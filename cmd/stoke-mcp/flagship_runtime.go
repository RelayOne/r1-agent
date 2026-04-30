package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type invoiceProcessorRuntimeInput struct {
	Accounts            []string `json:"accounts"`
	Destination         string   `json:"destination"`
	AlertUnpaidOverDays int      `json:"alert_unpaid_over_days"`
	WindowHours         int      `json:"window_hours"`
}

type invoiceProcessorRuntimeStep struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
	Skill   string `json:"skill"`
}

type invoiceProcessorApprovalRule struct {
	Condition string `json:"condition"`
	Action    string `json:"action"`
}

type invoiceProcessorRuntimeOutput struct {
	FlowSlug            string                         `json:"flow_slug"`
	Mode                string                         `json:"mode"`
	Accounts            []string                       `json:"accounts"`
	Destination         string                         `json:"destination"`
	Summary             string                         `json:"summary"`
	RequiredCredentials []string                       `json:"required_credentials"`
	HeroSkills          []string                       `json:"hero_skills"`
	Phases              []invoiceProcessorRuntimeStep  `json:"phases"`
	ApprovalRules       []invoiceProcessorApprovalRule `json:"approval_rules"`
}

func invoiceProcessorRuntime(input json.RawMessage) (json.RawMessage, error) {
	var req invoiceProcessorRuntimeInput
	if len(input) > 0 && string(input) != "null" {
		if err := json.Unmarshal(input, &req); err != nil {
			return nil, fmt.Errorf("decode input: %w", err)
		}
	}
	if len(req.Accounts) == 0 {
		return nil, fmt.Errorf("accounts must contain at least one inbox slug")
	}
	if req.WindowHours <= 0 {
		req.WindowHours = 24
	}
	if req.AlertUnpaidOverDays <= 0 {
		req.AlertUnpaidOverDays = 30
	}

	req.Destination = normalizeDestination(req.Destination)
	if req.Destination == "" {
		return nil, fmt.Errorf("destination must be one of quickbooks, google_sheets, or xero")
	}

	out := invoiceProcessorRuntimeOutput{
		FlowSlug:            "invoice-processor",
		Mode:                "basic",
		Accounts:            req.Accounts,
		Destination:         req.Destination,
		Summary:             buildInvoiceProcessorSummary(req),
		RequiredCredentials: requiredCredentialsForDestination(req.Destination),
		HeroSkills: []string{
			"classify_documents",
			"extract_structured_data",
			"reconcile_accounting",
		},
		Phases: []invoiceProcessorRuntimeStep{
			{
				Name:    "classify_documents",
				Skill:   "classify_documents",
				Purpose: fmt.Sprintf("Scan the last %d hours of mail in %d inboxes and isolate invoice or receipt candidates.", req.WindowHours, len(req.Accounts)),
			},
			{
				Name:    "extract_structured_data",
				Skill:   "extract_structured_data",
				Purpose: "Pull vendor, amount, invoice date, payment status, and line-item details into a normalized payload.",
			},
			{
				Name:    "reconcile_accounting",
				Skill:   "reconcile_accounting",
				Purpose: fmt.Sprintf("Write normalized records to %s and raise an operator approval when an unpaid invoice exceeds %d days.", req.Destination, req.AlertUnpaidOverDays),
			},
		},
		ApprovalRules: []invoiceProcessorApprovalRule{
			{
				Condition: "invoice amount exceeds 10000 USD equivalent",
				Action:    "pause write and request human approval",
			},
			{
				Condition: "vendor mismatch, duplicate invoice number, or missing destination mapping",
				Action:    "emit discrepancy summary and require operator review",
			},
		},
	}

	return json.Marshal(out)
}

func normalizeDestination(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "quickbooks":
		return "quickbooks"
	case "google_sheets", "google-sheets", "sheets":
		return "google_sheets"
	case "xero":
		return "xero"
	default:
		return ""
	}
}

func requiredCredentialsForDestination(destination string) []string {
	creds := []string{"gmail_oauth"}
	switch destination {
	case "quickbooks":
		return append(creds, "quickbooks_oauth")
	case "google_sheets":
		return append(creds, "google_sheets_oauth")
	case "xero":
		return append(creds, "xero_oauth")
	default:
		return creds
	}
}

func buildInvoiceProcessorSummary(req invoiceProcessorRuntimeInput) string {
	return fmt.Sprintf(
		"Process %d inboxes on a %d-hour lookback, extract invoice fields, then reconcile into %s with overdue alerts at %d days.",
		len(req.Accounts),
		req.WindowHours,
		req.Destination,
		req.AlertUnpaidOverDays,
	)
}
