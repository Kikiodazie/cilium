// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

//go:build ipam_provider_aws

package cmd

import (
	operatorOption "github.com/cilium/cilium/operator/option"
	"github.com/cilium/cilium/pkg/option"
)

func init() {
	flags := rootCmd.Flags()

	flags.Var(option.NewNamedMapOptions(operatorOption.AWSInstanceLimitMapping, &operatorOption.Config.AWSInstanceLimitMapping, nil),
		operatorOption.AWSInstanceLimitMapping,
		`Add or overwrite mappings of AWS instance limit in the form of `+
			`{"AWS instance type": "Maximum Network Interfaces","IPv4 Addresses `+
			`per Interface","IPv6 Addresses per Interface"}. cli example: `+
			`--aws-instance-limit-mapping=a1.medium=2,4,4 `+
			`--aws-instance-limit-mapping=a2.somecustomflavor=4,5,6 `+
			`configmap example: {"a1.medium": "2,4,4", "a2.somecustomflavor": "4,5,6"}`)
	regOpts.BindEnv(operatorOption.AWSInstanceLimitMapping)

	flags.Bool(operatorOption.AWSReleaseExcessIPs, false, "Enable releasing excess free IP addresses from AWS ENI.")
	regOpts.BindEnv(operatorOption.AWSReleaseExcessIPs)

	flags.Int(operatorOption.ExcessIPReleaseDelay, 180, "Number of seconds operator would wait before it releases an IP previously marked as excess")
	regOpts.BindEnv(operatorOption.ExcessIPReleaseDelay)

	flags.Bool(operatorOption.AWSEnablePrefixDelegation, false, "Allows operator to allocate prefixes to ENIs instead of individual IP addresses")
	regOpts.BindEnv(operatorOption.AWSEnablePrefixDelegation)

	flags.Var(option.NewNamedMapOptions(operatorOption.ENITags, &operatorOption.Config.ENITags, nil),
		operatorOption.ENITags, "ENI tags in the form of k1=v1 (multiple k/v pairs can be passed by repeating the CLI flag)")
	regOpts.BindEnv(operatorOption.ENITags)

	flags.Bool(operatorOption.UpdateEC2AdapterLimitViaAPI, false, "Use the EC2 API to update the instance type to adapter limits")
	regOpts.BindEnv(operatorOption.UpdateEC2AdapterLimitViaAPI)

	flags.Bool(operatorOption.AWSUsePrimaryAddress, false, "Allows for using primary address of the ENI for allocations on the node")
	regOpts.BindEnv(operatorOption.AWSUsePrimaryAddress)

	flags.String(operatorOption.EC2APIEndpoint, "", "AWS API endpoint for the EC2 service")
	regOpts.BindEnv(operatorOption.EC2APIEndpoint)

	Vp.BindPFlags(flags)
}
