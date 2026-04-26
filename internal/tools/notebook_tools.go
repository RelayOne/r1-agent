// notebook_tools.go — notebook_read and notebook_cell_run tool handlers.
//
// T-R1P-005: Jupyter notebook editing parity.
//
//   - notebook_read: reads a .ipynb file and returns a human-readable rendering
//     of all cells (type, source, and outputs). Supports JSON parsing from disk.
//   - notebook_cell_run: executes a single code cell via `jupyter nbconvert
//     --to script --execute` (graceful no-op when Jupyter is not installed,
//     returning a clear "jupyter not available" message so the model can fall
//     back to the bash tool).
//
// Both tools are path-confined to the registry working directory.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// notebookCellType mirrors the Jupyter cell_type field.
type notebookCellType string

const (
	nbCode     notebookCellType = "code"
	nbMarkdown notebookCellType = "markdown"
	nbRaw      notebookCellType = "raw"
)

// nbOutput is a simplified Jupyter output structure.
type nbOutput struct {
	OutputType string          `json:"output_type"`
	Text       json.RawMessage `json:"text"`
	Data       json.RawMessage `json:"data"`
	Traceback  []string        `json:"traceback"`
	EValue     string          `json:"evalue"`
}

// nbCell is a simplified Jupyter cell.
type nbCell struct {
	CellType  string     `json:"cell_type"`
	Source    interface{} `json:"source"` // string or []string
	Outputs   []nbOutput `json:"outputs"`
	ExecCount *int       `json:"execution_count"`
}

// nbNotebook is a minimal Jupyter notebook structure.
type nbNotebook struct {
	NBFormat int `json:"nbformat"`
	Cells    []nbCell `json:"cells"`
}

// extractStrings flattens a Jupyter source field (string or []string).
func extractStrings(v interface{}) string {
	if v == nil {
		return ""
	}
	switch sv := v.(type) {
	case string:
		return sv
	case []interface{}:
		var parts []string
		for _, item := range sv {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "")
	}
	return fmt.Sprintf("%v", v)
}

// handleNotebookRead implements the notebook_read tool (T-R1P-005).
func (r *Registry) handleNotebookRead(input json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	resolved, err := r.resolvePath(args.Path)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", args.Path, err)
	}

	var nb nbNotebook
	if err := json.Unmarshal(data, &nb); err != nil {
		return "", fmt.Errorf("cannot parse notebook %s: %w", args.Path, err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "notebook: %s (nbformat %d, %d cells)\n\n", resolved, nb.NBFormat, len(nb.Cells))

	for i, cell := range nb.Cells {
		execStr := ""
		if cell.CellType == string(nbCode) && cell.ExecCount != nil {
			execStr = fmt.Sprintf(" [%d]", *cell.ExecCount)
		}
		fmt.Fprintf(&sb, "## Cell %d — %s%s\n", i+1, cell.CellType, execStr)
		src := extractStrings(cell.Source)
		fmt.Fprintf(&sb, "%s\n", src)

		if len(cell.Outputs) > 0 {
			fmt.Fprintf(&sb, "### Outputs:\n")
			for _, out := range cell.Outputs {
				switch out.OutputType {
				case "stream", "display_data", "execute_result":
					text := extractStrings(nil)
					if out.Text != nil {
						var t interface{}
						if json.Unmarshal(out.Text, &t) == nil {
							text = extractStrings(t)
						}
					} else if out.Data != nil {
						var d map[string]interface{}
						if json.Unmarshal(out.Data, &d) == nil {
							if txt, ok := d["text/plain"]; ok {
								text = extractStrings(txt)
							}
						}
					}
					if text != "" {
						fmt.Fprintf(&sb, "%s\n", text)
					}
				case "error":
					fmt.Fprintf(&sb, "ERROR: %s\n", out.EValue)
					for _, tb := range out.Traceback {
						fmt.Fprintf(&sb, "  %s\n", tb)
					}
				}
			}
		}
		fmt.Fprintf(&sb, "\n")
	}

	return sb.String(), nil
}

// handleNotebookCellRun implements the notebook_cell_run tool (T-R1P-005).
// It appends the cell source as a new code cell to the notebook and executes
// the whole notebook in-place using `jupyter nbconvert --execute --inplace`.
// Gracefully returns "jupyter not available" when the binary is absent.
func (r *Registry) handleNotebookCellRun(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.Source) == "" {
		return "", fmt.Errorf("source is required")
	}

	resolved, err := r.resolvePath(args.Path)
	if err != nil {
		return "", err
	}

	// Check jupyter is available.
	if _, lookErr := exec.LookPath("jupyter"); lookErr != nil {
		return "notebook_cell_run: jupyter not available on this system. Use the bash tool to run Python directly, or install Jupyter (pip install jupyter).", nil
	}

	// Read the existing notebook (or create minimal empty one).
	var nb nbNotebook
	rawData, readErr := os.ReadFile(resolved)
	if readErr == nil {
		_ = json.Unmarshal(rawData, &nb)
	} else {
		// Create minimal notebook structure.
		nb = nbNotebook{
			NBFormat: 4,
		}
	}
	if nb.NBFormat == 0 {
		nb.NBFormat = 4
	}

	// Append the new cell.
	nb.Cells = append(nb.Cells, nbCell{
		CellType: string(nbCode),
		Source:   args.Source,
		Outputs:  []nbOutput{},
	})

	// Write back.
	encoded, marshalErr := json.MarshalIndent(nb, "", "  ")
	if marshalErr != nil {
		return "", fmt.Errorf("cannot marshal notebook: %w", marshalErr)
	}
	if writeErr := os.WriteFile(resolved, encoded, 0o600); writeErr != nil {
		return "", fmt.Errorf("cannot write notebook: %w", writeErr)
	}

	// Execute with jupyter nbconvert.
	execCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "jupyter", "nbconvert", "--to", "notebook", // #nosec G204 — binary is hardcoded
		"--execute", "--inplace", resolved)
	cmd.Dir = filepath.Dir(resolved)
	out, runErr := cmd.CombinedOutput()
	outStr := string(out)
	if len(outStr) > 4096 {
		outStr = outStr[:4096] + "\n...(truncated)"
	}
	if runErr != nil {
		return fmt.Sprintf("notebook_cell_run: execution failed\n%s\nerror: %v", outStr, runErr), nil
	}

	// Re-read notebook to get outputs from the new cell.
	result, readBackErr := r.handleNotebookRead(mustJSONRaw(map[string]string{"path": args.Path}))
	if readBackErr != nil {
		return fmt.Sprintf("notebook_cell_run: cell executed. notebook_read failed: %v", readBackErr), nil
	}
	return "notebook_cell_run: OK\n\n" + result, nil
}

// mustJSONRaw encodes v to json.RawMessage; panics on error.
func mustJSONRaw(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
