// mcp_search_tools.go — mcp_tool_search, mcp_resource_list, mcp_resource_read handlers.
//
// T-R1P-010: MCP tool search — discover tools available across connected MCP servers.
// T-R1P-011: MCP resource list/read — list and read named data resources from MCP servers.
//
// Architecture:
//   - The tools layer depends on the MCPToolSearcher interface (defined in tools.go)
//     rather than importing internal/mcp directly — avoids an import cycle and keeps
//     the dependency direction clean (tools does not pull in all of MCP).
//   - When no MCP registry is wired (r.mcpRegistry == nil) the tools return an
//     informational "unavailable" message like web_search does without TAVILY_API_KEY.
//   - mcp_resource_list and mcp_resource_read issue a graceful-degradation response
//     when the target server does not implement the Resources capability, consistent
//     with the MCP spec's optional-capability model.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// handleMCPToolSearch implements mcp_tool_search (T-R1P-010).
func (r *Registry) handleMCPToolSearch(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Query      string `json:"query"`
		Server     string `json:"server"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.Query) == "" {
		return "", fmt.Errorf("query is required")
	}
	if args.MaxResults <= 0 {
		args.MaxResults = 20
	}

	if r.mcpRegistry == nil {
		return "mcp_tool_search: no MCP registry configured — connect MCP servers via stoke.policy.yaml to enable tool discovery", nil
	}

	results, err := r.mcpRegistry.SearchTools(ctx, args.Query)
	if err != nil {
		return fmt.Sprintf("mcp_tool_search error: %v", err), nil
	}

	// Filter by server if requested.
	if args.Server != "" {
		var filtered []MCPToolSummary
		for _, t := range results {
			if strings.EqualFold(t.ServerName, args.Server) {
				filtered = append(filtered, t)
			}
		}
		results = filtered
	}

	if len(results) == 0 {
		return fmt.Sprintf("No MCP tools found matching %q", args.Query), nil
	}

	if len(results) > args.MaxResults {
		results = results[:args.MaxResults]
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d MCP tool(s) matching %q:\n\n", len(results), args.Query)
	for _, t := range results {
		fmt.Fprintf(&sb, "  mcp_%s_%s", t.ServerName, t.Name)
		if t.Description != "" {
			desc := t.Description
			if len(desc) > 120 {
				desc = desc[:120] + "..."
			}
			fmt.Fprintf(&sb, "\n    %s", desc)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\nCall these tools directly by their mcp_<server>_<name> identifier.")
	return sb.String(), nil
}

// handleMCPResourceList implements mcp_resource_list (T-R1P-011).
// Resource listing requires the MCP server to implement the Resources capability.
// When the capability is absent the tool returns a clear explanation.
func (r *Registry) handleMCPResourceList(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Server string `json:"server"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.Server) == "" {
		return "", fmt.Errorf("server is required")
	}

	if r.mcpRegistry == nil {
		return "mcp_resource_list: no MCP registry configured — connect MCP servers via stoke.policy.yaml first", nil
	}

	rl, ok := r.mcpRegistry.(MCPResourceLister)
	if !ok {
		return fmt.Sprintf("mcp_resource_list: connected MCP registry does not implement the Resources capability (server %q)", args.Server), nil
	}

	resources, err := rl.ListResources(ctx, args.Server)
	if err != nil {
		return fmt.Sprintf("mcp_resource_list error for server %q: %v", args.Server, err), nil
	}

	if len(resources) == 0 {
		return fmt.Sprintf("Server %q exposes no resources", args.Server), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Resources from %q (%d):\n\n", args.Server, len(resources))
	for _, res := range resources {
		fmt.Fprintf(&sb, "  %s", res.URI)
		if res.Name != "" && res.Name != res.URI {
			fmt.Fprintf(&sb, " (%s)", res.Name)
		}
		if res.Description != "" {
			desc := res.Description
			if len(desc) > 100 {
				desc = desc[:100] + "..."
			}
			fmt.Fprintf(&sb, "\n    %s", desc)
		}
		if res.MIMEType != "" {
			fmt.Fprintf(&sb, " [%s]", res.MIMEType)
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// handleMCPResourceRead implements mcp_resource_read (T-R1P-011).
func (r *Registry) handleMCPResourceRead(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Server string `json:"server"`
		URI    string `json:"uri"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(args.Server) == "" {
		return "", fmt.Errorf("server is required")
	}
	if strings.TrimSpace(args.URI) == "" {
		return "", fmt.Errorf("uri is required")
	}

	if r.mcpRegistry == nil {
		return "mcp_resource_read: no MCP registry configured — connect MCP servers via stoke.policy.yaml first", nil
	}

	rr, ok := r.mcpRegistry.(MCPResourceReader)
	if !ok {
		return fmt.Sprintf("mcp_resource_read: connected MCP registry does not implement the Resources capability (server %q)", args.Server), nil
	}

	content, mimeType, err := rr.ReadResource(ctx, args.Server, args.URI)
	if err != nil {
		return fmt.Sprintf("mcp_resource_read error for %q from server %q: %v", args.URI, args.Server, err), nil
	}

	const maxResourceBody = 512 * 1024
	if len(content) > maxResourceBody {
		content = content[:maxResourceBody]
		return fmt.Sprintf("Resource %s [%s] (truncated at 512KB):\n\n%s\n... [truncated]", args.URI, mimeType, content), nil
	}
	return fmt.Sprintf("Resource %s [%s]:\n\n%s", args.URI, mimeType, content), nil
}

// MCPResourceLister is an optional extension of MCPToolSearcher for servers
// that implement the Resources capability.
type MCPResourceLister interface {
	ListResources(ctx context.Context, serverName string) ([]MCPResource, error)
}

// MCPResourceReader reads the content of a named resource from an MCP server.
type MCPResourceReader interface {
	ReadResource(ctx context.Context, serverName, uri string) (content, mimeType string, err error)
}

// MCPResource describes a single resource advertised by an MCP server.
type MCPResource struct {
	URI         string
	Name        string
	Description string
	MIMEType    string
}
