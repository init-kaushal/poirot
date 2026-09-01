package main

import "github.com/spf13/cobra"

func newRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "poirot",
		Short:         "Kubernetes SRE assessment agent — point-in-time reliability/cost/change report",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(newVersionCmd(version))
	return root
}
