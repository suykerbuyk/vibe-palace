// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

// ResourceScheme is the URI scheme for vibe-palace content resources.
const ResourceScheme = "vibe-palace://"

// Resource URI templates (RFC 6570) registered with AddResourceTemplate.
// Each first path segment is a literal resource type; the {var} slots are
// filled by mcp-go from the matched URI and surfaced to the ResourceFunc.
const (
	TaskURITemplate      = ResourceScheme + "task/{project}/{slug}"
	ResumeURITemplate    = ResourceScheme + "resume/{project}"
	WorkflowURITemplate  = ResourceScheme + "workflow/{project}"
	CommandURITemplate   = ResourceScheme + "command/{project}/{name}"
	SkillURITemplate     = ResourceScheme + "skill/{project}/{name}"
	SessionURITemplate   = ResourceScheme + "session/{project}/{session_id}"
	KnowledgeURITemplate = ResourceScheme + "knowledge/{project}"
	LearningURITemplate  = ResourceScheme + "learning/{slug}"
)

// TaskURI builds the canonical URI for a task body.
func TaskURI(project, slug string) string {
	return ResourceScheme + "task/" + project + "/" + slug
}

// ResumeURI builds the canonical URI for a project's resume body.
func ResumeURI(project string) string {
	return ResourceScheme + "resume/" + project
}

// WorkflowURI builds the canonical URI for a project's workflow body.
func WorkflowURI(project string) string {
	return ResourceScheme + "workflow/" + project
}

// CommandURI builds the canonical URI for a command body.
func CommandURI(project, name string) string {
	return ResourceScheme + "command/" + project + "/" + name
}

// SkillURI builds the canonical URI for a skill's SKILL.md body.
func SkillURI(project, name string) string {
	return ResourceScheme + "skill/" + project + "/" + name
}

// SessionURI builds the canonical URI for a session body.
func SessionURI(project, id string) string {
	return ResourceScheme + "session/" + project + "/" + id
}

// KnowledgeURI builds the canonical URI for a project's knowledge body.
func KnowledgeURI(project string) string {
	return ResourceScheme + "knowledge/" + project
}

// LearningURI builds the canonical URI for a vault-wide learning body.
func LearningURI(slug string) string {
	return ResourceScheme + "learning/" + slug
}
