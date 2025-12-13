package oxigraph

import (
	"errors"
	"io"
)

var ErrInvalidData = errors.New("invalid binary data")

type decoder struct {
	data []byte
	pos  int
}

func (d *decoder) readVarint() (int, error) {
	var result int
	var shift uint
	for {
		if d.pos >= len(d.data) {
			return 0, io.ErrUnexpectedEOF
		}
		b := d.data[d.pos]
		d.pos++
		result |= int(b&0x7f) << shift
		if b&0x80 == 0 {
			break
		}
		shift += 7
	}
	return result, nil
}

func (d *decoder) readBytes(n int) ([]byte, error) {
	if d.pos+n > len(d.data) {
		return nil, io.ErrUnexpectedEOF
	}
	b := d.data[d.pos : d.pos+n]
	d.pos += n
	return b, nil
}

func (d *decoder) readByte() (byte, error) {
	if d.pos >= len(d.data) {
		return 0, io.ErrUnexpectedEOF
	}
	b := d.data[d.pos]
	d.pos++
	return b, nil
}

func (d *decoder) readTerm() (Term, error) {
	tag, err := d.readByte()
	if err != nil {
		return nil, err
	}

	switch tag {
	case 0x01: // NamedNode
		length, err := d.readVarint()
		if err != nil {
			return nil, err
		}
		iri, err := d.readBytes(length)
		if err != nil {
			return nil, err
		}
		return NamedNode{IRI: string(iri)}, nil

	case 0x02: // BlankNode
		length, err := d.readVarint()
		if err != nil {
			return nil, err
		}
		id, err := d.readBytes(length)
		if err != nil {
			return nil, err
		}
		return BlankNode{ID: string(id)}, nil

	case 0x03: // SimpleLiteral
		length, err := d.readVarint()
		if err != nil {
			return nil, err
		}
		value, err := d.readBytes(length)
		if err != nil {
			return nil, err
		}
		return Literal{Val: string(value), Datatype: "http://www.w3.org/2001/XMLSchema#string"}, nil

	case 0x04: // LangLiteral
		valueLen, err := d.readVarint()
		if err != nil {
			return nil, err
		}
		value, err := d.readBytes(valueLen)
		if err != nil {
			return nil, err
		}
		langLen, err := d.readVarint()
		if err != nil {
			return nil, err
		}
		lang, err := d.readBytes(langLen)
		if err != nil {
			return nil, err
		}
		return Literal{Val: string(value), Language: string(lang)}, nil

	case 0x05: // TypedLiteral
		valueLen, err := d.readVarint()
		if err != nil {
			return nil, err
		}
		value, err := d.readBytes(valueLen)
		if err != nil {
			return nil, err
		}
		dtLen, err := d.readVarint()
		if err != nil {
			return nil, err
		}
		dt, err := d.readBytes(dtLen)
		if err != nil {
			return nil, err
		}
		return Literal{Val: string(value), Datatype: string(dt)}, nil

	default:
		return nil, ErrInvalidData
	}
}

func decodeSolution(data []byte) (Solution, error) {
	d := &decoder{data: data}

	numBindings, err := d.readVarint()
	if err != nil {
		return Solution{}, err
	}

	vars := make([]string, 0, numBindings)
	bindings := make(map[string]Term, numBindings)
	for range numBindings {
		varLen, err := d.readVarint()
		if err != nil {
			return Solution{}, err
		}
		varName, err := d.readBytes(varLen)
		if err != nil {
			return Solution{}, err
		}
		term, err := d.readTerm()
		if err != nil {
			return Solution{}, err
		}
		name := string(varName)
		vars = append(vars, name)
		bindings[name] = term
	}

	return NewOrderedSolution(vars, bindings), nil
}

func decodeQuad(data []byte) (Quad, error) {
	d := &decoder{data: data}

	// Subject
	subj, err := d.readTerm()
	if err != nil {
		return Quad{}, err
	}

	// Predicate (must be NamedNode)
	pred, err := d.readTerm()
	if err != nil {
		return Quad{}, err
	}
	predNN, ok := pred.(NamedNode)
	if !ok {
		return Quad{}, ErrInvalidData
	}

	// Object
	obj, err := d.readTerm()
	if err != nil {
		return Quad{}, err
	}

	// Graph
	graphTag, err := d.readByte()
	if err != nil {
		return Quad{}, err
	}

	var graph Term
	if graphTag == 0x00 {
		// Default graph
		graph = nil
	} else {
		// Put the tag back for readTerm
		d.pos--
		graph, err = d.readTerm()
		if err != nil {
			return Quad{}, err
		}
	}

	return Quad{
		Subject:   subj,
		Predicate: predNN,
		Object:    obj,
		Graph:     graph,
	}, nil
}
