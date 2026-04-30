package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type dentistOutreachRuntimeInput struct {
	Markets       []string `json:"markets"`
	Location      string   `json:"location"`
	CRM           string   `json:"crm"`
	DailyNewLeads int      `json:"daily_new_leads"`
	SequenceDays  int      `json:"sequence_days"`
}

type dentistOutreachRuntimeStep struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
	Skill   string `json:"skill"`
}

type dentistOutreachApprovalRule struct {
	Condition string `json:"condition"`
	Action    string `json:"action"`
}

type dentistOutreachRuntimeOutput struct {
	FlowSlug            string                        `json:"flow_slug"`
	Mode                string                        `json:"mode"`
	Markets             []string                      `json:"markets"`
	Location            string                        `json:"location"`
	CRM                 string                        `json:"crm"`
	Summary             string                        `json:"summary"`
	RequiredCredentials []string                      `json:"required_credentials"`
	HeroSkills          []string                      `json:"hero_skills"`
	Phases              []dentistOutreachRuntimeStep  `json:"phases"`
	ApprovalRules       []dentistOutreachApprovalRule `json:"approval_rules"`
}

func dentistOutreachRuntime(input json.RawMessage) (json.RawMessage, error) {
	var req dentistOutreachRuntimeInput
	if len(input) > 0 && string(input) != "null" {
		if err := json.Unmarshal(input, &req); err != nil {
			return nil, fmt.Errorf("decode input: %w", err)
		}
	}
	if len(req.Markets) == 0 {
		return nil, fmt.Errorf("markets must contain at least one dental service line")
	}
	req.Location = strings.TrimSpace(req.Location)
	if req.Location == "" {
		return nil, fmt.Errorf("location must be provided")
	}
	if req.DailyNewLeads <= 0 {
		req.DailyNewLeads = 25
	}
	if req.SequenceDays <= 0 {
		req.SequenceDays = 14
	}

	req.CRM = normalizeCRM(req.CRM)
	if req.CRM == "" {
		return nil, fmt.Errorf("crm must be one of hubspot, google_sheets, or salesforce")
	}

	out := dentistOutreachRuntimeOutput{
		FlowSlug:            "dentist-outreach",
		Mode:                "basic",
		Markets:             req.Markets,
		Location:            req.Location,
		CRM:                 req.CRM,
		Summary:             buildDentistOutreachSummary(req),
		RequiredCredentials: requiredCredentialsForCRM(req.CRM),
		HeroSkills: []string{
			"brave_search",
			"hunter_io",
			"clearbit_enrich",
			"gmail_draft",
			"hubspot_create",
		},
		Phases: []dentistOutreachRuntimeStep{
			{
				Name:    "source_local_practices",
				Skill:   "brave_search",
				Purpose: fmt.Sprintf("Find %s dental practices that match %s service lines and build the first-pass lead list.", req.Location, strings.Join(req.Markets, ", ")),
			},
			{
				Name:    "enrich_contacts",
				Skill:   "hunter_io",
				Purpose: fmt.Sprintf("Resolve decision-maker emails and enrich each practice until the batch reaches %d new leads.", req.DailyNewLeads),
			},
			{
				Name:    "draft_personalized_outreach",
				Skill:   "gmail_draft",
				Purpose: fmt.Sprintf("Generate personalized email drafts with a %d-day follow-up sequence and keep every send in approval-required state.", req.SequenceDays),
			},
			{
				Name:    "log_pipeline",
				Skill:   "hubspot_create",
				Purpose: fmt.Sprintf("Write approved leads, notes, and outreach state into %s for pipeline tracking.", req.CRM),
			},
		},
		ApprovalRules: []dentistOutreachApprovalRule{
			{
				Condition: "before any outbound email send",
				Action:    "require human approval on the drafted message",
			},
			{
				Condition: "before any CRM write that creates or mutates a practice record",
				Action:    "require operator approval and surface the exact field diff",
			},
		},
	}

	return json.Marshal(out)
}

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

func normalizeCRM(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "hubspot":
		return "hubspot"
	case "google_sheets", "google-sheets", "sheets":
		return "google_sheets"
	case "salesforce":
		return "salesforce"
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

func requiredCredentialsForCRM(crm string) []string {
	creds := []string{"hunter_oauth", "google_oauth"}
	switch crm {
	case "hubspot":
		return append(creds, "hubspot_oauth")
	case "google_sheets":
		return append(creds, "google_sheets_oauth")
	case "salesforce":
		return append(creds, "salesforce_oauth")
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

func buildDentistOutreachSummary(req dentistOutreachRuntimeInput) string {
	return fmt.Sprintf(
		"Source %d dental service lines in %s, enrich up to %d new leads per batch, draft approval-gated outreach, and sync pipeline state into %s over a %d-day sequence.",
		len(req.Markets),
		req.Location,
		req.DailyNewLeads,
		req.CRM,
		req.SequenceDays,
	)
}
