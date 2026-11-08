package main

import "ofam.dev/ochazuke"

// Namespace is the singular namespace o-gh declares (lexicon plan): its
// operations are gh.<noun>.<verb> and a host mounts its catalog under it.
const Namespace = "gh"

func init() {
	if err := ochazuke.DeclareNamespace(Namespace); err != nil {
		panic(err)
	}
}
