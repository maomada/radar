package search

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// TrivyGroup is the API group Trivy Operator writes its reports in. Its
// reports have no spec or status: every finding lives under a top-level
// `report` field.
const TrivyGroup = "aquasecurity.github.io"

// kindRBACGroups are API groups whose namespaced kinds search reads only where
// the caller may list that kind (Options.KindAccess). Seeing a namespace's
// Pods doesn't mean being allowed to read its scan findings, and Trivy
// reports carry CVE and exposed-secret findings.
var kindRBACGroups = map[string]bool{TrivyGroup: true}

// maxTrivyFieldBytes bounds one indexed report entry. IDs and versions are
// short; the cap only guards against a malformed report.
const maxTrivyFieldBytes = 256

// trivyReportContent is the searchable face of a Trivy Operator report.
//
// Only identifiers are indexed: CVE IDs with package and versions, SBOM
// component names and versions, failed check IDs and secret rule IDs.
// Severity words, titles and descriptions are left out because generic tokens
// ("high", "memory", "root") would otherwise match hundreds of reports and
// drown ordinary searches; severity stays reachable through the `report` CEL
// binding. A secret finding's `match` holds the matched line and is never
// indexed. Entries are not capped: a capped report would silently miss the
// CVE a caller is looking for.
func trivyReportContent(report map[string]any) []ContentField {
	if report == nil {
		return nil
	}
	vulns := asSlice(report["vulnerabilities"])
	components := asSlice(asMap(report["components"])["components"])
	checks := asSlice(report["checks"])
	secrets := asSlice(report["secrets"])
	out := make([]ContentField, 0, len(vulns)+len(components)+len(checks)+len(secrets))
	add := func(prefix string, i int, value string) {
		if value != "" {
			out = append(out, ContentField{Path: prefix + "[" + strconv.Itoa(i) + "]", Value: value})
		}
	}

	for i, item := range vulns {
		v := asMap(item)
		id := asString(v["vulnerabilityID"])
		if id == "" {
			continue
		}
		fixed := asString(v["fixedVersion"])
		if fixed == "" {
			fixed = "unfixed"
		}
		add("report.vulnerabilities", i, joinField(id, asString(v["resource"]), asString(v["installedVersion"]), "→", fixed))
	}
	for i, item := range components {
		c := asMap(item)
		add("report.components.components", i, joinField(asString(c["name"]), asString(c["version"])))
	}
	for i, item := range checks {
		c := asMap(item)
		if success, ok := c["success"].(bool); !ok || success {
			continue
		}
		add("report.checks", i, joinField(asString(c["checkID"])))
	}
	for i, item := range secrets {
		s := asMap(item)
		add("report.secrets", i, joinField(asString(s["ruleID"]), asString(s["target"])))
	}
	return out
}

// joinField joins the non-empty parts with spaces into one indexed value,
// capped at maxTrivyFieldBytes on a rune boundary. Search walks reports on
// every query, so this allocates once per value.
func joinField(parts ...string) string {
	n := 0
	for _, p := range parts {
		if p != "" {
			n += len(p) + 1
		}
	}
	if n == 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(n)
	for _, p := range parts {
		if p == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(p)
	}
	v := b.String()
	if len(v) > maxTrivyFieldBytes {
		cut := maxTrivyFieldBytes
		for cut > 0 && !utf8.RuneStart(v[cut]) {
			cut--
		}
		v = v[:cut]
	}
	return v
}

// trivyArtifactImage renders the scanned image as a pullable reference, so
// `image:` modifiers and free tokens match reports the way they match Pods.
func trivyArtifactImage(report map[string]any) string {
	artifact := asMap(report["artifact"])
	repo := asString(artifact["repository"])
	if repo == "" {
		return ""
	}
	ref := repo
	if server := asString(asMap(report["registry"])["server"]); server != "" {
		ref = server + "/" + repo
	}
	if tag := asString(artifact["tag"]); tag != "" {
		ref += ":" + tag
	}
	if digest := asString(artifact["digest"]); digest != "" {
		ref += "@" + digest
	}
	return ref
}

// trivyReportProjection is what the `report` CEL binding exposes: the fields
// findings are identified and ranked by, in the report's own shape so an
// expression written against the object (report.components.components, …)
// works unchanged. It is a copy, never the cached object, and it leaves out
// every free-text field, in particular a secret finding's `match`, so a
// filter can't be used to probe secret values one predicate at a time.
func trivyReportProjection(report map[string]any) map[string]any {
	out := map[string]any{}
	if report == nil {
		return out
	}
	if ts := asString(report["updateTimestamp"]); ts != "" {
		out["updateTimestamp"] = ts
	}
	if v := pick(asMap(report["artifact"]), "repository", "tag", "digest"); len(v) > 0 {
		out["artifact"] = v
	}
	if v := pick(asMap(report["registry"]), "server"); len(v) > 0 {
		out["registry"] = v
	}
	if v := pick(asMap(report["os"]), "family", "name"); len(v) > 0 {
		out["os"] = v
	}
	if v := pick(asMap(report["scanner"]), "name", "version"); len(v) > 0 {
		out["scanner"] = v
	}
	if summary := asMap(report["summary"]); len(summary) > 0 {
		counts := make(map[string]any, len(summary))
		for k, v := range summary {
			switch v.(type) {
			case int64, float64, int:
				counts[k] = v
			}
		}
		out["summary"] = counts
	}
	if v := pickEach(report["vulnerabilities"], "vulnerabilityID", "resource", "installedVersion", "fixedVersion", "severity", "score"); v != nil {
		out["vulnerabilities"] = v
	}
	if v := pickEach(asMap(report["components"])["components"], "name", "version", "purl", "type"); v != nil {
		out["components"] = map[string]any{"components": v}
	}
	if v := pickEach(report["checks"], "checkID", "severity", "success", "category"); v != nil {
		out["checks"] = v
	}
	if v := pickEach(report["secrets"], "ruleID", "category", "severity", "target"); v != nil {
		out["secrets"] = v
	}
	return out
}

func pick(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch v.(type) {
			case string, bool, int64, float64, int:
				out[k] = v
			}
		}
	}
	return out
}

func pickEach(v any, keys ...string) []any {
	items := asSlice(v)
	if items == nil {
		return nil
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, pick(asMap(item), keys...))
	}
	return out
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
