// oxigraph-poc demonstrates the oxigraph Go bindings with iter.Seq.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/mbrock/swash/pkg/oxigraph"
)

func main() {
	ctx := context.Background()

	rt, err := oxigraph.NewRuntime(ctx)
	if err != nil {
		log.Fatalf("failed to create runtime: %v", err)
	}
	defer rt.Close()
	fmt.Println("✓ Runtime initialized")

	store, err := rt.NewStore()
	if err != nil {
		log.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()
	fmt.Println("✓ Store created")

	ntriples := `<http://example.org/alice> <http://xmlns.com/foaf/0.1/name> "Alice" .
<http://example.org/alice> <http://xmlns.com/foaf/0.1/knows> <http://example.org/bob> .
<http://example.org/bob> <http://xmlns.com/foaf/0.1/name> "Bob" .
<http://example.org/bob> <http://xmlns.com/foaf/0.1/age> "30"^^<http://www.w3.org/2001/XMLSchema#integer> .
`

	if err := store.LoadString(ntriples, oxigraph.NTriples); err != nil {
		log.Fatalf("failed to load data: %v", err)
	}
	fmt.Println("✓ Loaded N-Triples data")

	count, err := store.Len()
	if err != nil {
		log.Fatalf("failed to get store length: %v", err)
	}
	fmt.Printf("✓ Store contains %d quads\n", count)

	fmt.Println("\n--- SPARQL Query (iter.Seq2) ---")
	query := `SELECT ?name WHERE { ?person <http://xmlns.com/foaf/0.1/name> ?name }`
	for solution, err := range store.Query(query) {
		if err != nil {
			log.Fatalf("query error: %v", err)
		}
		if name, ok := solution.Get("name"); ok {
			fmt.Printf("  name = %s (value: %q)\n", name, name.Value())
		}
	}

	fmt.Println("\n--- All Quads (iter.Seq) ---")
	for quad := range store.All() {
		fmt.Printf("  %s\n", quad)
	}

	fmt.Println("\n--- Pattern Match: Alice as subject ---")
	alice := oxigraph.IRI("http://example.org/alice")
	for quad := range store.Quads(oxigraph.Pattern{Subject: alice}) {
		fmt.Printf("  %s %s %s\n", quad.Subject, quad.Predicate, quad.Object)
	}

	fmt.Println("\n--- Pattern Match: foaf:name predicate ---")
	foafName := oxigraph.IRI("http://xmlns.com/foaf/0.1/name")
	for quad := range store.Quads(oxigraph.Pattern{Predicate: &foafName}) {
		fmt.Printf("  %s -> %s\n", quad.Subject.Value(), quad.Object.Value())
	}

	fmt.Println("\n--- Multiple Stores ---")
	store2, err := rt.NewStore()
	if err != nil {
		log.Fatalf("failed to create second store: %v", err)
	}
	defer store2.Close()

	store2.LoadString(`<http://example.org/charlie> <http://xmlns.com/foaf/0.1/name> "Charlie" .`, oxigraph.NTriples)
	count2, _ := store2.Len()
	fmt.Printf("  Store 1 has %d quads, Store 2 has %d quads\n", count, count2)

	fmt.Println("\n✓ Done!")
}
