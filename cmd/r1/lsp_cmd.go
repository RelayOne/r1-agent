package main

// lsp_cmd.go — `r1 lsp` subcommand.
//
// T-R1P-009: LSP server adapter. Launches R1 as a Language Server
// Protocol server over stdin/stdout. Any LSP-enabled editor can
// connect by setting r1 as the language server binary:
//
//   VSCode settings.json:
//     "r1.lsp.command": ["r1", "lsp", "--root", "${workspaceFolder}"]
//
//   Neovim (lspconfig):
//     require('lspconfig').r1.setup({ cmd = { "r1", "lsp", "--root", vim.loop.cwd() } })
//
// Capabilities provided:
//   textDocument/completion  — symbol completions from the repo index
//   textDocument/hover       — doc comment + signature for the symbol under cursor
//   textDocument/definition  — jump-to-definition
//   workspace/symbol         — workspace-wide symbol search
//
// The server builds a symbol index lazily on the first request that
// needs it, so startup is fast and the first symbol request may be
// slightly slower.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/RelayOne/r1/internal/lsp"
)

func lspCmd(args []string) {
	fs := flag.NewFlagSet("lsp", flag.ExitOnError)
	root := fs.String("root", "", "Project root directory (default: current directory)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: r1 lsp [--root DIR]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Start R1 as a Language Server Protocol (LSP) server.")
		fmt.Fprintln(os.Stderr, "Communicates over stdin/stdout using JSON-RPC 2.0 with")
		fmt.Fprintln(os.Stderr, "the standard Content-Length framing.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if *root == "" {
		var err error
		// LINT-ALLOW chdir-cli-entry: r1 lsp subcommand; cwd captured once before lsp.NewServer and never re-read inside request handlers (see internal/lsp pass-4 refactor).
		*root, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "r1 lsp: getwd: %v\n", err)
			os.Exit(1)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	srv := lsp.NewServer(*root)
	fmt.Fprintf(os.Stderr, "r1 lsp: started (root=%s)\n", *root)

	if err := srv.Serve(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "r1 lsp: %v\n", err)
		os.Exit(1)
	}
}
