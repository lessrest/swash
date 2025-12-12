package oxigraph

import (
	"fmt"
	"strings"
)

// TermType identifies the kind of RDF term.
type TermType int

const (
	TermNamedNode TermType = iota
	TermBlankNode
	TermLiteral
)

// Term represents an RDF term (IRI, blank node, or literal).
type Term interface {
	TermType() TermType
	Value() string
	String() string // N-Triples serialization
}

// NamedNode is an IRI reference.
type NamedNode struct {
	IRI string
}

func (n NamedNode) TermType() TermType { return TermNamedNode }
func (n NamedNode) Value() string      { return n.IRI }
func (n NamedNode) String() string     { return "<" + n.IRI + ">" }

// IRI creates a NamedNode from an IRI string.
func IRI(iri string) NamedNode {
	return NamedNode{IRI: iri}
}

// BlankNode is a blank node.
type BlankNode struct {
	ID string
}

func (b BlankNode) TermType() TermType { return TermBlankNode }
func (b BlankNode) Value() string      { return b.ID }
func (b BlankNode) String() string     { return "_:" + b.ID }

// Literal is an RDF literal.
type Literal struct {
	Val      string
	Language string // empty if typed
	Datatype string // IRI, defaults to xsd:string
}

func (l Literal) TermType() TermType { return TermLiteral }
func (l Literal) Value() string      { return l.Val }
func (l Literal) String() string {
	escaped := strings.ReplaceAll(l.Val, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
	escaped = strings.ReplaceAll(escaped, "\n", "\\n")
	escaped = strings.ReplaceAll(escaped, "\r", "\\r")

	if l.Language != "" {
		return fmt.Sprintf("\"%s\"@%s", escaped, l.Language)
	}
	if l.Datatype != "" && l.Datatype != "http://www.w3.org/2001/XMLSchema#string" {
		return fmt.Sprintf("\"%s\"^^<%s>", escaped, l.Datatype)
	}
	return fmt.Sprintf("\"%s\"", escaped)
}

// StringLiteral creates a simple string literal.
func StringLiteral(value string) Literal {
	return Literal{Val: value, Datatype: "http://www.w3.org/2001/XMLSchema#string"}
}

// LangLiteral creates a language-tagged literal.
func LangLiteral(value, lang string) Literal {
	return Literal{Val: value, Language: lang}
}

// TypedLiteral creates a typed literal.
func TypedLiteral(value, datatype string) Literal {
	return Literal{Val: value, Datatype: datatype}
}

// Quad represents an RDF quad (triple + optional graph).
type Quad struct {
	Subject   Term
	Predicate NamedNode
	Object    Term
	Graph     Term // nil = default graph
}

func (q Quad) String() string {
	if q.Graph != nil {
		return fmt.Sprintf("%s %s %s %s .", q.Subject, q.Predicate, q.Object, q.Graph)
	}
	return fmt.Sprintf("%s %s %s .", q.Subject, q.Predicate, q.Object)
}

// Solution is a single row of bindings from a SELECT query.
type Solution struct {
	bindings map[string]Term
}

// Get returns the term bound to a variable.
func (s Solution) Get(variable string) (Term, bool) {
	t, ok := s.bindings[variable]
	return t, ok
}

// Variables returns all variable names in this solution.
func (s Solution) Variables() []string {
	vars := make([]string, 0, len(s.bindings))
	for v := range s.bindings {
		vars = append(vars, v)
	}
	return vars
}
