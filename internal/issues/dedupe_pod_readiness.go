package issues

func dedupePodReadyEchoOverNative(in []Issue) []Issue {
	isPod := func(i Issue) bool { return i.Group == "" && i.Kind == "Pod" }
	in = structuralRootOverSymptom(in,
		func(i Issue) bool {
			return isPod(i) && i.Source == SourceProblem && i.Reason == "ReadinessGatesNotReady"
		},
		func(i Issue) bool {
			return isPod(i) && i.Source == SourceCondition && i.Reason == "Ready: ReadinessGatesNotReady"
		},
	)
	return structuralRootOverSymptom(in,
		func(i Issue) bool {
			return isPod(i) && (i.Source == SourceProblem || i.Source == SourceScheduling || i.Source == SourceMissingRef)
		},
		func(i Issue) bool {
			return isPod(i) && i.Source == SourceCondition && (i.Reason == "Ready: ContainersNotReady" || i.Reason == "Ready: PodFailed")
		},
	)
}
