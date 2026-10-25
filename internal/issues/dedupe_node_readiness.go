package issues

import "strings"

func dedupeNodeReadyEchoOverRemoval(in []Issue) []Issue {
	return structuralRootOverSymptom(in,
		func(i Issue) bool {
			return i.Group == "" && i.Kind == "Node" && i.Source == SourceProblem && i.Reason == "Removal delayed"
		},
		func(i Issue) bool {
			return i.Group == "" && i.Kind == "Node" && i.Source == SourceCondition && (strings.HasPrefix(i.Reason, "Ready: ") || i.Reason == "Ready=False")
		},
	)
}
