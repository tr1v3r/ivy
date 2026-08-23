package driver

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// check interface
var _ Driver = (*XMLDriver)(nil)

// NewXMLDriver create a new xml driver
func NewXMLDriver() *XMLDriver {
	return &XMLDriver{
		PathParser: SlashPathParser,
		Realizer:   new(StdRealizer),
		Modem: &GeneralModem[*XMLProcessor]{
			Marshaler:   json.Marshal,
			Unmarshaler: json.Unmarshal,
		},
	}
}

// XMLDriver is a driver for XML type rule tree
type XMLDriver struct {
	PathParser
	Realizer
	Modem
}

// Name return driver name
func (XMLDriver) Name() string { return "xml" }

var _ Processor = (*XMLProcessor)(nil)

// XMLProcessor is a Processor for XML type rule tree
type XMLProcessor struct {
	// P is the target path of the Processor
	P string `json:"path"`

	// T is the type of the Processor
	T string `json:"type"`
	// XMLPath is the xml element path of the Processor (slash-separated)
	XMLPath string `json:"xml_path"`
	// V is the value of the Processor
	V []byte `json:"value"`

	// A is the author of the Processor
	A string `json:"author"`
	// C is the create time of the Processor
	C time.Time `json:"created_at"`
}

// Type returns the operation type (create/set/replace/delete).
func (op *XMLProcessor) Type() string { return op.T }

// Path returns the target tree path of the Processor.
func (op *XMLProcessor) Path() string { return op.P }

// Author returns the processor author.
func (op *XMLProcessor) Author() string { return op.A }

// CreatedAt returns the processor creation time.
func (op *XMLProcessor) CreatedAt() time.Time { return op.C }

// Load populates the processor from its JSON serialization.
func (op *XMLProcessor) Load(data []byte) error {
	if err := json.Unmarshal(data, op); err != nil {
		return fmt.Errorf("unmarshal fail: %w", err)
	}
	return nil
}

// Save returns the JSON serialization of the processor.
func (op *XMLProcessor) Save() []byte {
	data, _ := json.Marshal(op)
	return data
}

// Process applies the typed operation to the XML document.
func (op *XMLProcessor) Process(_ *RealizeContext, before []byte) (after []byte, err error) {
	if len(before) == 0 {
		before = []byte(`<root/>`)
	}
	root, err := xmlToNodes(before)
	if err != nil {
		return nil, fmt.Errorf("parse xml fail: %w", err)
	}

	segments := splitXMLPath(op.XMLPath)

	switch op.T {
	case "create", "append":
		err = xmlCreate(root, segments, op.V)
	case "set":
		err = xmlSet(root, segments, op.V)
	case "replace":
		err = xmlReplace(root, segments, op.V)
	case "delete":
		err = xmlDelete(root, segments)
	default:
		return nil, fmt.Errorf("unknown Processor type: %s", op.T)
	}
	if err != nil {
		return nil, err
	}

	return nodesToXML(root)
}

// --- internal XML node tree ---

// xmlNode represents a node in an XML document tree.
type xmlNode struct {
	Name     xml.Name
	Attr     []xml.Attr
	Children []*xmlNode
	Text     string
}

// xmlToNodes parses XML bytes into an xmlNode tree.
// The returned root node is a synthetic container whose first child is the document root element.
func xmlToNodes(data []byte) (*xmlNode, error) {
	root := &xmlNode{}
	stack := []*xmlNode{root}

	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			child := &xmlNode{Name: t.Name, Attr: t.Attr}
			top := stack[len(stack)-1]
			top.Children = append(top.Children, child)
			stack = append(stack, child)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			text := strings.TrimSpace(string(t))
			if text != "" && len(stack) > 0 {
				top := stack[len(stack)-1]
				if top.Text == "" {
					top.Text = text
				} else {
					top.Text += " " + text
				}
			}
		}
	}

	return root, nil
}

// nodesToXML serializes an xmlNode tree back to XML bytes.
func nodesToXML(root *xmlNode) ([]byte, error) {
	buf := new(bytes.Buffer)
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(buf)
	for _, child := range root.Children {
		if err := writeXMLNode(enc, child); err != nil {
			return nil, err
		}
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeXMLNode(enc *xml.Encoder, node *xmlNode) error {
	start := xml.StartElement{Name: node.Name, Attr: node.Attr}
	if err := enc.EncodeToken(start); err != nil {
		return err
	}
	if node.Text != "" {
		if err := enc.EncodeToken(xml.CharData(node.Text)); err != nil {
			return err
		}
	}
	for _, child := range node.Children {
		if err := writeXMLNode(enc, child); err != nil {
			return err
		}
	}
	if err := enc.EncodeToken(xml.EndElement{Name: node.Name}); err != nil {
		return err
	}
	return nil
}

// --- path helpers ---

func splitXMLPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

// xmlNavigate finds the element at the given path segments under root.
// Root is the synthetic container; segments[0] matches the document root element.
func xmlNavigate(root *xmlNode, segments []string) (*xmlNode, error) {
	if len(segments) == 0 {
		return nil, fmt.Errorf("empty xml path")
	}
	cur := root
	for _, seg := range segments {
		found := false
		for _, child := range cur.Children {
			if child.Name.Local == seg {
				cur = child
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("element not found: %s", seg)
		}
	}
	return cur, nil
}

// xmlNavigateOrCreate finds the element at path, creating missing intermediates.
func xmlNavigateOrCreate(root *xmlNode, segments []string) (*xmlNode, error) {
	if len(segments) == 0 {
		return nil, fmt.Errorf("empty xml path")
	}
	cur := root
	for _, seg := range segments {
		found := false
		for _, child := range cur.Children {
			if child.Name.Local == seg {
				cur = child
				found = true
				break
			}
		}
		if !found {
			newChild := &xmlNode{Name: xml.Name{Local: seg}}
			cur.Children = append(cur.Children, newChild)
			cur = newChild
		}
	}
	return cur, nil
}

// xmlNavigateParent returns the parent node and the final segment name.
func xmlNavigateParent(root *xmlNode, segments []string) (*xmlNode, string, error) {
	if len(segments) == 0 {
		return nil, "", fmt.Errorf("empty xml path")
	}
	parentSegments := segments[:len(segments)-1]
	lastName := segments[len(segments)-1]

	parent := root
	if len(parentSegments) > 0 {
		var err error
		parent, err = xmlNavigate(root, parentSegments)
		if err != nil {
			return nil, "", err
		}
	}
	return parent, lastName, nil
}

// --- operations ---

// xmlCreate appends a new child element at the given path.
// Intermediate elements are created if they don't exist.
func xmlCreate(root *xmlNode, segments []string, value []byte) error {
	if len(segments) == 0 {
		return fmt.Errorf("empty xml path")
	}

	// Navigate to parent, creating intermediates
	parentSegments := segments[:len(segments)-1]
	lastName := segments[len(segments)-1]

	parent := root
	if len(parentSegments) > 0 {
		var err error
		parent, err = xmlNavigateOrCreate(root, parentSegments)
		if err != nil {
			return err
		}
	}

	// Append new child element
	child := &xmlNode{Name: xml.Name{Local: lastName}}
	if len(value) > 0 {
		// Try parsing value as XML; if it works, use children; otherwise use as text
		parsed, err := xmlToNodes(value)
		if err == nil && len(parsed.Children) > 0 {
			child.Children = parsed.Children
		} else {
			child.Text = string(value)
		}
	}
	parent.Children = append(parent.Children, child)
	return nil
}

// xmlSet sets text content at the given path, creating intermediates if needed.
func xmlSet(root *xmlNode, segments []string, value []byte) error {
	node, err := xmlNavigateOrCreate(root, segments)
	if err != nil {
		return err
	}
	node.Text = string(value)
	return nil
}

// xmlReplace replaces the content of the element at the given path.
func xmlReplace(root *xmlNode, segments []string, value []byte) error {
	node, err := xmlNavigate(root, segments)
	if err != nil {
		return err
	}
	node.Children = nil
	node.Text = ""
	if len(value) > 0 {
		parsed, err := xmlToNodes(value)
		if err == nil && len(parsed.Children) > 0 {
			node.Children = parsed.Children
		} else {
			node.Text = string(value)
		}
	}
	return nil
}

// xmlDelete removes the element at the given path.
func xmlDelete(root *xmlNode, segments []string) error {
	parent, lastName, err := xmlNavigateParent(root, segments)
	if err != nil {
		return err
	}
	for i, child := range parent.Children {
		if child.Name.Local == lastName {
			parent.Children = append(parent.Children[:i], parent.Children[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("element not found: %s", lastName)
}
