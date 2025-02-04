package bpf

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
)

const globalDataMap = ".data"

// LoadCollectionSpec loads the eBPF ELF at the given path and parses it into
// a CollectionSpec. This spec is only a blueprint of the contents of the ELF
// and does not represent any live resources that have been loaded into the
// kernel.
//
// This is a wrapper around ebpf.LoadCollectionSpec that parses legacy iproute2
// bpf_elf_map definitions (only used for prog_arrays at the time of writing)
// and assigns tail calls annotated with `__section_tail` macros to their
// intended maps and slots.
func LoadCollectionSpec(path string) (*ebpf.CollectionSpec, error) {
	spec, err := ebpf.LoadCollectionSpec(path)
	if err != nil {
		return nil, err
	}

	if err := iproute2Compat(spec); err != nil {
		return nil, err
	}

	classifyProgramTypes(spec)

	return spec, nil
}

// iproute2Compat parses the Extra field of each MapSpec in the CollectionSpec.
// This extra portion is present in legacy bpf_elf_map definitions and must be
// handled before the map can be loaded into the kernel.
//
// It parses the ELF section name of each ProgramSpec to extract any map/slot
// mappings for prog arrays used as tail call maps. The spec's programs are then
// inserted into the appropriate map and slot.
//
// TODO(timo): Remove when bpf_elf_map map definitions are no longer used after
// moving away from iproute2+libbpf.
func iproute2Compat(spec *ebpf.CollectionSpec) error {
	// Parse legacy iproute2 u32 id and pinning fields.
	maps := make(map[uint32]*ebpf.MapSpec)
	for _, m := range spec.Maps {
		if m.Extra != nil && m.Extra.Len() > 0 {
			tail := struct {
				ID      uint32
				Pinning uint32
				_       uint64 // inner_id + inner_idx
			}{}
			if err := binary.Read(m.Extra, spec.ByteOrder, &tail); err != nil {
				return fmt.Errorf("reading iproute2 map definition: %w", err)
			}

			if tail.Pinning > 0 {
				m.Pinning = 1 // LIBBPF_PIN_BY_NAME
			}

			// Index maps by their iproute2 .id if any, so X/Y ELF section names can
			// be matched against them.
			if tail.ID != 0 {
				if m2 := maps[tail.ID]; m2 != nil {
					return fmt.Errorf("maps %s and %s have duplicate iproute2 map ID %d", m.Name, m2.Name, tail.ID)
				}
				maps[tail.ID] = m
			}
		}
	}

	for n, p := range spec.Programs {
		// Parse the program's section name to determine which prog array and slot it
		// needs to be inserted into. For example, a section name of '2/14' means to
		// insert into the map with the .id field of 2 at index 14.
		// Uses %v to automatically detect slot's mathematical base, since they can
		// appear either in dec or hex, e.g. 1/0x0515.
		var id, slot uint32
		if _, err := fmt.Sscanf(p.SectionName, "%d/%v", &id, &slot); err == nil {
			// Assign the prog name and slot to the map with the iproute2 .id obtained
			// from the program's section name. The lib will load the ProgramSpecs
			// and insert the corresponding Programs into the prog array at load time.
			m := maps[id]
			if m == nil {
				return fmt.Errorf("no map with iproute2 map .id %d", id)
			}
			m.Contents = append(maps[id].Contents, ebpf.MapKV{Key: slot, Value: n})
		}
	}

	return nil
}

// LoadCollection loads the given spec into the kernel with the specified opts.
//
// Any maps marked as pinned in the spec are automatically loaded from the path
// given in opts.Maps.PinPath and will be used instead of creating new ones.
// MapSpecs that differ (type/key/value/max/flags) from their pinned versions
// will result in an ebpf.ErrMapIncompatible here and the map must be removed
// before loading the CollectionSpec.
func LoadCollection(spec *ebpf.CollectionSpec, opts ebpf.CollectionOptions) (*ebpf.Collection, error) {
	if spec == nil {
		return nil, errors.New("can't load nil CollectionSpec")
	}

	// By default, allocate a 1MiB verifier log buffer if first load attempt
	// fails. This was adjusted around Cilium 1.11 for fitting bpf_lxc insn
	// limit messages.
	if opts.Programs.LogSize == 0 {
		opts.Programs.LogSize = 1 << 20
	}

	// Copy spec so the modifications below don't affect the input parameter,
	// allowing the spec to be safely re-used by the caller.
	spec = spec.Copy()

	if err := inlineGlobalData(spec); err != nil {
		return nil, fmt.Errorf("inlining global data: %w", err)
	}

	coll, err := ebpf.NewCollectionWithOptions(spec, opts)
	if err != nil {
		return nil, err
	}

	return coll, nil
}

// classifyProgramTypes sets the type of ProgramSpecs which the library cannot
// automatically classify due to them being in unrecognized ELF sections. Only
// programs of type UnspecifiedProgram are modified.
//
// Cilium uses the iproute2 X/Y section name convention for assigning programs
// to prog array slots, which is also not supported.
func classifyProgramTypes(spec *ebpf.CollectionSpec) {
	// Assign a program type based on the first recognized function name.
	var t ebpf.ProgramType
	for name := range spec.Programs {
		switch name {
		// bpf_xdp.c
		case "cil_xdp_entry":
			t = ebpf.XDP
		case
			// bpf_lxc.c
			"cil_from_container", "cil_to_container",
			// bpf_host.c
			"cil_from_netdev", "cil_from_host", "cil_to_netdev", "cil_to_host",
			// bpf_network.c
			"cil_from_network",
			// bpf_overlay.c
			"cil_to_overlay", "cil_from_overlay":
			t = ebpf.SchedCLS
		default:
			continue
		}

		break
	}

	for _, p := range spec.Programs {
		if p.Type == ebpf.UnspecifiedProgram {
			p.Type = t
		}
	}
}

// inlineGlobalData replaces all map loads from a global data section with
// immediate dword loads, effectively performing those map lookups in the
// loader. This is done for compatibility with kernels that don't support
// global data maps yet.
//
// Currently, all map reads are expected to be 32 bits wide until BTF MapKV
// can be fully accessed by the caller, which would allow for querying value
// widths.
//
// This works in conjunction with the __fetch macros in the datapath, which
// emit direct array accesses instead of memory loads with an offset from the
// map's pointer.
func inlineGlobalData(spec *ebpf.CollectionSpec) error {
	data, err := globalData(spec)
	if err != nil {
		return err
	}
	if data == nil {
		// No static data, nothing to replace.
		return nil
	}

	// Don't attempt to create an empty map .bss in the kernel.
	delete(spec.Maps, ".bss")

	for _, prog := range spec.Programs {
		for i, ins := range prog.Instructions {
			if !ins.IsLoadFromMap() || ins.Src != asm.PseudoMapValue {
				continue
			}

			// The compiler inserts relocations for .bss for zero values.
			if ins.Reference() == ".bss" {
				prog.Instructions[i] = asm.LoadImm(ins.Dst, 0, asm.DWord)
				continue
			}

			if ins.Reference() != globalDataMap {
				return fmt.Errorf("global constants must be in %s, but found reference to %s", globalDataMap, ins.Reference())
			}

			// Get the offset of the read within the target map,
			// stored in the 32 most-significant bits of Constant.
			// Equivalent to Instruction.mapOffset().
			off := uint32(uint64(ins.Constant) >> 32)

			if off%4 != 0 {
				return fmt.Errorf("global const access at offset %d not 32-bit aligned", off)
			}

			imm := spec.ByteOrder.Uint32(data[off : off+4])

			// Replace the map load with an immediate load. Must be a dword load
			// to match the instruction width of a map load.
			prog.Instructions[i] = asm.LoadImm(ins.Dst, int64(imm), asm.DWord)
		}
	}

	return nil
}

// globalData gets the contents of the first entry in the global data map
// and removes it from the spec to prevent it from being created in the kernel.
func globalData(spec *ebpf.CollectionSpec) ([]byte, error) {
	data := spec.Maps[globalDataMap]
	if data == nil {
		return nil, nil
	}

	if dl := len(data.Contents); dl != 1 {
		return nil, fmt.Errorf("expected one key in %s, found %d", globalDataMap, dl)
	}

	out, ok := (data.Contents[0].Value).([]byte)
	if !ok {
		return nil, fmt.Errorf("expected %s value to be a byte slice, got: %T",
			globalDataMap, data.Contents[0].Value)
	}

	// Remove the map definition to skip loading it into the kernel.
	delete(spec.Maps, globalDataMap)

	return out, nil
}
