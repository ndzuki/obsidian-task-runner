package cli

import (
	"fmt"
	"net/http"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/vaultweb"
	"github.com/spf13/cobra"
)

var (
	webAddr    string
	webMapFile string
)

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Serve the read-only vault dashboard HTTP API for the DSH web plugin",
	Long: `Starts a read-only JSON API over the Obsidian vault (projects, tasks,
whitelisted Dataview projections, design library) for the DSH Web dashboard
plugin. Writes are not exposed here; they arrive in a later phase behind
TaskStore.Apply generation fencing.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(webMapFile)
		if err != nil {
			return err
		}
		if cfg.ObsidianVault == "" {
			return fmt.Errorf("obsidian_vault not configured")
		}
		srv := &http.Server{
			Addr:              webAddr,
			Handler:           vaultweb.New(cfg.ObsidianVault).Handler(),
			ReadHeaderTimeout: 5 * time.Second,
		}
		cmd.Printf("vault dashboard API listening on http://%s\n", webAddr)
		return srv.ListenAndServe()
	},
}

func init() {
	webCmd.Flags().StringVar(&webAddr, "addr", "127.0.0.1:8787", "Listen address for the vault dashboard API")
	webCmd.Flags().StringVar(&webMapFile, "map-file", "", "Path to vault-map.json")
	rootCmd.AddCommand(webCmd)
}
