package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/init-kaushal/poirot/internal/config"
	"github.com/init-kaushal/poirot/internal/orchestrator"
	"github.com/init-kaushal/poirot/internal/report"
)

func newRunCmd(version string) *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Assess a cluster and write report.json + report.md",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			res, err := orchestrator.Run(cmd.Context(), orchestrator.Options{Config: cfg, Version: version})
			if err != nil {
				return err
			}
			if err := writeOutputs(cfg.Output.Dir, res.Report); err != nil {
				return err
			}
			c := res.Report.Meta.Counts
			fmt.Fprintf(cmd.OutOrStdout(),
				"wrote %s/report.json and %s/report.md — %d critical, %d warning, %d info\n",
				cfg.Output.Dir, cfg.Output.Dir, c.Critical, c.Warning, c.Info)
			if res.ExitCode != 0 {
				return &ExitError{Code: res.ExitCode}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&cfgPath, "config", "c", "poirot.yaml", "path to the poirot config file")
	return cmd
}

func writeOutputs(dir string, r report.Report) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	j, err := r.JSON()
	if err != nil {
		return fmt.Errorf("render report json: %w", err)
	}
	jsonPath := filepath.Join(dir, "report.json")
	if err := os.WriteFile(jsonPath, j, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", jsonPath, err)
	}
	m, err := r.Markdown()
	if err != nil {
		return fmt.Errorf("render report markdown: %w", err)
	}
	mdPath := filepath.Join(dir, "report.md")
	if err := os.WriteFile(mdPath, m, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", mdPath, err)
	}
	return nil
}
