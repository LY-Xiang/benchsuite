package claim

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"lyxiang/benchsuite/src/text"
)

// isFieldName reports whether s is a bound field name: an identifier, anchored at
// both ends. It is a byte scan rather than a regexp because it runs once per
// name on every case, and the pattern is a plain character-class walk.
func isFieldName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z'):
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// A claim travels as the raw JSON bytes the generator wrote. The generator writes
// both the input and the claim, so the validator re-derives every fact from those
// bytes: a typed struct would drop a key it does not understand and pass a claim
// the bytes contradict.
//
// A claim node is one JSON object with every key it carries. `tok` and `repeat`
// stay raw because each may be an integer literal or a bound field name, and only
// a raw value tells those apart.
type claimNode map[string]json.RawMessage

// quote renders a string single-quoted, as these messages name offending keys.

var errNotObject = errors.New("not a JSON object")

// walkObject calls fn for each key in document order, which the error messages
// depend on.
func walkObject(raw json.RawMessage, fn func(key string, value json.RawMessage) error) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return errNotObject
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := keyTok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
		if err := fn(key, value); err != nil {
			return err
		}
	}
	return nil
}

// An operand is either an integer literal or the name of a bound field. Telling
// those apart in place avoids boxing every operand in the claim, which is the
// validator's hottest path.
//
// The literal keeps its text rather than a parsed int: the shape pass accepts any
// integer literal, and only the value pass needs a number that fits in an int.
type operand struct {
	name   string
	number string
	isName bool
}

// decodeOperand classifies one raw operand. Anything else -- bool, null, array,
// object, a float -- is refused.
//
// The bytes are read directly rather than through a json.Decoder: one fresh
// decoder per two-digit number costs far more than the value it decodes. Only a
// string that actually carries an escape falls back to the decoder.
func decodeOperand(raw json.RawMessage) (operand, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return operand{}, false
	}
	if trimmed[0] == '"' {
		name, ok := decodeJSONString(trimmed)
		if !ok {
			return operand{}, false
		}
		return operand{name: name, isName: true}, true
	}
	if !isIntegerText(trimmed) {
		return operand{}, false
	}
	return operand{number: string(trimmed)}, true
}

// isIntegerText reports whether b is a JSON integer literal: an optional minus
// followed by digits, with no leading zero, no fraction and no exponent. A float
// is not an integer literal even when it reads like one.
func isIntegerText(b []byte) bool {
	if len(b) > 0 && b[0] == '-' {
		b = b[1:]
	}
	if len(b) == 0 || (len(b) > 1 && b[0] == '0') {
		return false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// decodeJSONString decodes one complete JSON string literal. The common case --
// a bare field name -- is unquoted in place; anything with a backslash is handed
// to the decoder, which owns the escape table.
func decodeJSONString(text []byte) (string, bool) {
	if len(text) < 2 || text[len(text)-1] != '"' {
		return "", false
	}
	body := text[1 : len(text)-1]
	if bytes.IndexByte(body, '\\') < 0 {
		// No escapes, so JSON validity reduces to "no raw control byte".
		for _, c := range body {
			if c < 0x20 {
				return "", false
			}
		}
		return string(body), true
	}
	var name string
	if err := json.Unmarshal(text, &name); err != nil {
		return "", false
	}
	return name, true
}

func isIntegerLiteral(raw json.RawMessage) (int, bool) {
	value, ok := decodeOperand(raw)
	if !ok || value.isName {
		return 0, false
	}
	n, err := strconv.Atoi(value.number)
	if err != nil {
		return 0, false
	}
	return n, true
}

func isName(raw json.RawMessage) (string, bool) {
	value, ok := decodeOperand(raw)
	if !ok || !value.isName || !isFieldName(value.name) {
		return "", false
	}
	return value.name, true
}

func checkOperand(raw json.RawMessage, label string) error {
	if _, ok := decodeOperand(raw); !ok {
		return fmt.Errorf("%s must be an integer or a bound field name", label)
	}
	return nil
}

var lineNodeKeys = map[string]bool{"tok": true, "field": true, "range": true, "len": true, "bytes": true}

// checkClaimNodes refuses a claim shape the harness cannot re-derive.
//
// bound is the enclosing scope, copied here: names bound by the caller are
// visible in this subtree, but nothing bound here escapes to the caller.
func checkClaimNodes(nodes []parsedNode, bound map[string]bool) error {
	scope := make(map[string]bool, len(bound))
	for name := range bound {
		scope[name] = true
	}

	for _, parsed := range nodes {
		if parsed.objErr != nil {
			return parsed.objErr
		}
		if parsed.isRep {
			if err := checkRepeatNode(parsed, scope); err != nil {
				return err
			}
			continue
		}
		if err := checkLineNode(parsed.node, scope); err != nil {
			return err
		}
	}
	return nil
}

// checkRepeatNode validates a repeat node's key set, its count, then its body, in
// that order: a node missing `lines` is refused as a malformed repeat rather than
// as a line list.
func checkRepeatNode(parsed parsedNode, scope map[string]bool) error {
	node := parsed.node
	repeat := node["repeat"]
	// The key set must be exactly {repeat, lines}.
	if _, hasLines := node["lines"]; len(node) != 2 || !hasLines {
		return fmt.Errorf("repeat node must contain only repeat and lines")
	}
	if err := checkOperand(repeat, "repeat"); err != nil {
		return err
	}
	if count, ok := isIntegerLiteral(repeat); ok && count < 0 {
		return fmt.Errorf("repeat cannot be negative")
	}
	if name, ok := isName(repeat); ok && !scope[name] {
		return fmt.Errorf("repeat references unbound field %s", text.Quote(name))
	}
	// A repeat body binds for itself only: names it introduces must not
	// escape into the enclosing scope.
	if parsed.bodyErr != nil {
		return parsed.bodyErr
	}
	return checkClaimNodes(parsed.body, scope)
}

// checkLineNode validates one line node and binds its fields into scope, so the
// names it binds are visible to every node after this one.
func checkLineNode(node claimNode, scope map[string]bool) error {
	var unknown []string
	for key := range node {
		if !lineNodeKeys[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return fmt.Errorf("unknown claim node key %s", text.Quote(unknown[0]))
	}

	tok, hasTok := node["tok"]
	if !hasTok {
		return fmt.Errorf("claim line is missing tok")
	}
	if err := checkOperand(tok, "tok"); err != nil {
		return err
	}
	if count, ok := isIntegerLiteral(tok); ok && count < 0 {
		return fmt.Errorf("tok cannot be negative")
	}
	if name, ok := isName(tok); ok && !scope[name] {
		return fmt.Errorf("tok references unbound field %s", text.Quote(name))
	}

	rawFields, hasFields := node["field"]
	var fields []string
	if hasFields {
		decoded, ok := decodeNameList(rawFields)
		if !ok {
			return fmt.Errorf("field must be a list of one name per token")
		}
		for _, name := range decoded {
			if !isFieldName(name) {
				return fmt.Errorf("field must be a list of one name per token")
			}
		}
		fields = decoded
		if count, ok := isIntegerLiteral(tok); ok && len(fields) != count {
			return fmt.Errorf("len(field)=%d does not equal tok=%d", len(fields), count)
		}
	}

	lineScope := make(map[string]bool, len(scope)+len(fields))
	for name := range scope {
		lineScope[name] = true
	}
	for _, name := range fields {
		lineScope[name] = true
	}

	if rawLen, ok := node["len"]; ok {
		name, valid := isName(rawLen)
		if !valid {
			return fmt.Errorf("len must name a bound field")
		}
		if !lineScope[name] {
			return fmt.Errorf("len references unbound field %s", text.Quote(name))
		}
	}

	if rawBytes, ok := node["bytes"]; ok {
		if !validByteBounds(rawBytes) {
			return fmt.Errorf("bytes must be an ordered pair in [0, 255]")
		}
	}

	if rawRange, ok := node["range"]; ok {
		if err := checkRange(rawRange, fields, lineScope, hasFields); err != nil {
			return err
		}
	}

	for _, name := range fields {
		scope[name] = true
	}
	return nil
}

func validByteBounds(raw json.RawMessage) bool {
	var bounds []json.RawMessage
	if err := json.Unmarshal(raw, &bounds); err != nil || len(bounds) != 2 {
		return false
	}
	lower, ok := isIntegerLiteral(bounds[0])
	if !ok {
		return false
	}
	upper, ok := isIntegerLiteral(bounds[1])
	if !ok {
		return false
	}
	return lower >= 0 && upper <= 255 && lower <= upper
}

// isJSONObject reports whether raw is a JSON object.
func isJSONObject(raw json.RawMessage) bool {
	return walkObject(raw, func(string, json.RawMessage) error { return nil }) == nil
}

func checkRange(raw json.RawMessage, fields []string, lineScope map[string]bool, hasFields bool) error {
	if !isJSONObject(raw) {
		return fmt.Errorf("range must be an object")
	}
	if !hasFields {
		return fmt.Errorf("range requires fields bound on the same line")
	}
	bound := make(map[string]bool, len(fields))
	for _, name := range fields {
		bound[name] = true
	}
	return walkObject(raw, func(name string, rawBounds json.RawMessage) error {
		if !isFieldName(name) || !bound[name] {
			return fmt.Errorf("range field %s is not bound on this line", text.Quote(name))
		}
		var bounds []json.RawMessage
		if err := json.Unmarshal(rawBounds, &bounds); err != nil || len(bounds) != 2 {
			return fmt.Errorf("range for %s must have two bounds", text.Quote(name))
		}
		if err := checkOperand(bounds[0], "range lower bound for "+name); err != nil {
			return err
		}
		if !isJSONNull(bounds[1]) {
			if err := checkOperand(bounds[1], "range upper bound for "+name); err != nil {
				return err
			}
		}
		for _, boundRaw := range bounds {
			boundName, ok := isName(boundRaw)
			if ok && !lineScope[boundName] {
				return fmt.Errorf("range for %s references unbound field %s",
					text.Quote(name), text.Quote(boundName))
			}
		}
		return nil
	})
}

func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// decodeNameList reads a `field` value, which must be a JSON array of strings.
// null is refused explicitly, since it would otherwise read as an empty list and
// be reported as a token-count mismatch instead of a malformed field.
func decodeNameList(raw json.RawMessage) ([]string, bool) {
	if isJSONNull(raw) {
		return nil, false
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		return nil, false
	}
	names := make([]string, 0, len(elements))
	for _, element := range elements {
		var name string
		if err := json.Unmarshal(element, &name); err != nil {
			return nil, false
		}
		names = append(names, name)
	}
	return names, true
}

// appendFields appends the whitespace-separated fields of s to dst. It is
// strings.FieldsSeq into a caller-owned buffer: the same Unicode space set, so
// the token boundaries do not change, and no slice per line.
func appendFields(dst []string, s string) []string {
	if s == "" {
		return dst
	}
	for field := range strings.FieldsSeq(s) {
		dst = append(dst, field)
	}
	return dst
}

// resolveInteger re-derives an operand as a number, following a field name back
// to the token text bound to it.
func resolveInteger(raw json.RawMessage, fields map[string]string, label string) (int, error) {
	if value, ok := isIntegerLiteral(raw); ok {
		return value, nil
	}
	name, ok := isName(raw)
	if !ok {
		return 0, fmt.Errorf("%s must be an integer or field reference", label)
	}
	bound, ok := fields[name]
	if !ok {
		return 0, fmt.Errorf("%s references unbound field %s", label, text.Quote(name))
	}
	value, err := strconv.Atoi(bound)
	if err != nil {
		return 0, fmt.Errorf("field %s is not an integer", text.Quote(name))
	}
	return value, nil
}

// inputLines splits a generated input into its lines. One final newline
// terminates the last line; a further one is a blank line, which is a gate-6
// failure, not something to trim.
func inputLines(text string) ([]string, error) {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	if strings.Contains(normalized, "\r") {
		return nil, fmt.Errorf("input contains a bare carriage return")
	}
	lines := strings.Split(normalized, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines, nil
}

// ValidateClaim re-derives a structural claim from the input text. Every failure
// is gate 6: the generator wrote both sides, so a failing claim is the
// generator's bug.
func ValidateClaim(input, claim []byte) error {
	_, err := validateClaim(input, claim)
	return err
}

// bindings is what one validating walk learns besides pass/fail: the tokens bound
// to each field name, and the outermost repeat count, which `expect` calls
// `cases`.
type bindings struct {
	values    map[string][]string
	caseCount int // -1 until a repeat is seen
}

// validateClaim also hands back the bindings the walk collected, so `expect` and
// the validator share one walk.
//
// The claim is decoded once. The shape pass and the value pass below read the
// same decoded nodes, so a claim is never parsed against the same bytes twice.
func validateClaim(input, claim []byte) (*bindings, error) {
	nodes, err := parseClaim(claim)
	if err != nil {
		return nil, err
	}
	if err := checkClaimNodes(nodes, nil); err != nil {
		return nil, err
	}
	lines, err := inputLines(string(input))
	if err != nil {
		return nil, err
	}
	collected := &bindings{values: map[string][]string{}, caseCount: -1}
	w := &walker{lines: lines, collector: collected}
	final, err := w.validateNodes(nodes, 0, map[string]string{})
	if err != nil {
		return nil, err
	}
	if final != len(lines) {
		return nil, fmt.Errorf("input has %d extra line(s) after the last one", len(lines)-final)
	}
	return collected, nil
}

// parsedNode is one claim node after decoding: its object keys and, for a
// repeat, its decoded body list. Decoding the tree costs one pass over the
// claim, and both later walks use the result.
//
// A malformed node or body is recorded rather than returned, because the two
// walks report a parent's own refusal before descending into it. Returning early
// from the decode would surface a child's error where its parent's belongs.
type parsedNode struct {
	node   claimNode
	objErr error
	isRep  bool
	body   []parsedNode
	// bodyErr is non-nil when a repeat's `lines` could not be read as a list.
	bodyErr error
}

// parseClaim decodes the claim into nodes. The top level must be exactly
// `{"lines": [...]}`.
func parseClaim(claim []byte) ([]parsedNode, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(claim, &top); err != nil || len(top) != 1 {
		return nil, fmt.Errorf("claim must be an object containing only lines")
	}
	rawLines, ok := top["lines"]
	if !ok {
		return nil, fmt.Errorf("claim must be an object containing only lines")
	}
	return parseNodes(rawLines)
}

// parseNodes decodes one node list. JSON null decodes into a Go slice as a no-op,
// so it is refused explicitly: otherwise `lines: null` validates with zero nodes
// and is waved through.
func parseNodes(raw json.RawMessage) ([]parsedNode, error) {
	var raws []json.RawMessage
	if err := json.Unmarshal(raw, &raws); err != nil || isJSONNull(raw) {
		return nil, fmt.Errorf("claim lines must be a list")
	}
	nodes := make([]parsedNode, 0, len(raws))
	for _, rawNode := range raws {
		var parsed parsedNode
		if err := json.Unmarshal(rawNode, &parsed.node); err != nil {
			parsed.objErr = fmt.Errorf("each claim node must be an object")
			nodes = append(nodes, parsed)
			continue
		}
		if _, isRepeat := parsed.node["repeat"]; isRepeat {
			parsed.isRep = true
			body, err := parseNodes(parsed.node["lines"])
			if err != nil {
				parsed.bodyErr = err
			}
			parsed.body = body
		}
		nodes = append(nodes, parsed)
	}
	return nodes, nil
}

// walker carries the state one validating walk needs. It holds a reusable token
// slice because every line is re-split on the same buffer: a case is hundreds of
// lines, and a fresh slice per line is the walk's largest remaining allocation.
type walker struct {
	lines     []string
	collector *bindings
	tokens    []string
	names     []string
	// undo records the scope changes a repeat body made, so the enclosing
	// iteration can rewind them without copying the whole scope.
	undo []rebinding
}

// validateNodes walks a node list against the input, returning the line it
// stopped at. A node carrying both keys is read as a repeat.
func (w *walker) validateNodes(nodes []parsedNode, index int,
	fields map[string]string) (int, error) {
	for _, parsed := range nodes {
		if parsed.objErr != nil {
			return index, parsed.objErr
		}

		var next int
		var err error
		if parsed.isRep {
			next, err = w.validateRepeat(parsed, index, fields)
		} else {
			next, err = w.validateLine(parsed.node, index, fields)
		}
		if err != nil {
			return next, err
		}
		index = next
	}
	return index, nil
}

// validateRepeat expands a repeat body count times, returning the line it stopped
// at. The count and body are resolved before any iteration, so a malformed repeat
// is refused before the input is touched.
func (w *walker) validateRepeat(parsed parsedNode, index int,
	fields map[string]string) (int, error) {
	count, err := resolveInteger(parsed.node["repeat"], fields, "repeat")
	if err != nil {
		return index, err
	}
	if count < 0 {
		return index, fmt.Errorf("repeat count %d is negative", count)
	}
	// `cases` is the outer repeat's count: the first one the walk meets.
	if w.collector.caseCount < 0 {
		w.collector.caseCount = count
	}
	if parsed.bodyErr != nil {
		return index, parsed.bodyErr
	}
	body := parsed.body
	for range count {
		// Each iteration rebinds its own names; none survives into the next.
		// The scope is rewound rather than copied: a copy per iteration is the
		// walk's largest remaining allocation, and the names a body binds are
		// exactly the ones the undo list records.
		undo := w.bind()
		index, err = w.validateNodes(body, index, fields)
		w.rebind(fields, undo)
		if err != nil {
			return index, err
		}
	}
	return index, nil
}

// rebinding is one scope change the walk must undo: the name and the token text
// it held before, if any.
type rebinding struct {
	name    string
	old     string
	existed bool
}

// bind starts recording the scope changes a repeat body makes and returns a mark
// to pass to rebind. Nested repeats stack: the mark is the buffer length at this
// point, so an inner body rewinds only its own changes. Marking by length rather
// than by slice keeps the buffer's growth across iterations.
func (w *walker) bind() int {
	return len(w.undo)
}

// rebind rewinds every change recorded since the matching bind.
func (w *walker) rebind(fields map[string]string, mark int) {
	for i := len(w.undo) - 1; i >= mark; i-- {
		change := w.undo[i]
		if change.existed {
			fields[change.name] = change.old
			continue
		}
		delete(fields, change.name)
	}
	w.undo = w.undo[:mark]
}

// setBound binds name to value, recording the previous binding so the enclosing
// repeat iteration can rewind it.
func (w *walker) setBound(fields map[string]string, name, value string) {
	old, existed := fields[name]
	w.undo = append(w.undo, rebinding{name: name, old: old, existed: existed})
	fields[name] = value
}

// validateLine checks one input line and binds its names into fields, which the
// caller shares, so a name bound here is usable by the next node.
func (w *walker) validateLine(node claimNode, index int,
	fields map[string]string) (int, error) {
	if index >= len(w.lines) {
		return index, fmt.Errorf(
			"claim requires more lines than the file has (missing line %d)", index+1)
	}
	tokens := appendFields(w.tokens[:0], w.lines[index])
	w.tokens = tokens
	expected, err := resolveInteger(node["tok"], fields, "tok")
	if err != nil {
		return index, err
	}
	if expected < 0 {
		return index, fmt.Errorf("tok count %d is negative", expected)
	}
	if len(tokens) != expected {
		return index, fmt.Errorf("line %d has %d token(s), the claim says %d",
			index+1, len(tokens), expected)
	}

	if rawFields, ok := node["field"]; ok {
		// Decoding into the walker's slice reuses its capacity: a case binds
		// names on nearly every line, and a fresh slice per line is the walk's
		// largest remaining allocation.
		w.names = w.names[:0]
		if err := json.Unmarshal(rawFields, &w.names); err != nil {
			return index, fmt.Errorf("field must be a list of one name per token")
		}
		if len(w.names) != len(tokens) {
			return index, fmt.Errorf("line %d has %d token(s), but field binds %d",
				index+1, len(tokens), len(w.names))
		}
		for i, name := range w.names {
			w.setBound(fields, name, tokens[i])
			w.collector.values[name] = append(w.collector.values[name], tokens[i])
		}
	}

	if rawRange, ok := node["range"]; ok {
		err := walkObject(rawRange, func(name string, rawBounds json.RawMessage) error {
			var bounds []json.RawMessage
			if err := json.Unmarshal(rawBounds, &bounds); err != nil || len(bounds) != 2 {
				return fmt.Errorf("range for %s must have two bounds", text.Quote(name))
			}
			value, err := resolveInteger(json.RawMessage(strconv.Quote(name)), fields, "range field")
			if err != nil {
				return err
			}
			lower, err := resolveInteger(bounds[0], fields, "range lower bound")
			if err != nil {
				return err
			}
			upper := 0
			hasUpper := false
			if !isJSONNull(bounds[1]) {
				upper, err = resolveInteger(bounds[1], fields, "range upper bound")
				if err != nil {
					return err
				}
				hasUpper = true
			}
			if value < lower || (hasUpper && value > upper) {
				upperText := "no upper bound"
				if hasUpper {
					upperText = strconv.Itoa(upper)
				}
				return fmt.Errorf("%s=%d is outside [%d, %s]", name, value, lower, upperText)
			}
			return nil
		})
		if err != nil {
			return index, err
		}
	}

	if rawLen, ok := node["len"]; ok {
		if len(tokens) != 1 {
			return index, fmt.Errorf("len requires exactly one token")
		}
		expectedLength, err := resolveInteger(rawLen, fields, "len")
		if err != nil {
			return index, err
		}
		if len(tokens[0]) != expectedLength {
			return index, fmt.Errorf("token has %d characters, the claim says %d",
				len(tokens[0]), expectedLength)
		}
	}

	if rawBytes, ok := node["bytes"]; ok {
		if len(tokens) != 1 {
			return index, fmt.Errorf("bytes requires exactly one token")
		}
		var bounds []int
		if err := json.Unmarshal(rawBytes, &bounds); err != nil || len(bounds) != 2 {
			return index, fmt.Errorf("bytes must be an ordered pair in [0, 255]")
		}
		for _, b := range []byte(tokens[0]) {
			if int(b) < bounds[0] || int(b) > bounds[1] {
				return index, fmt.Errorf("byte %d is outside [%d, %d]", b, bounds[0], bounds[1])
			}
		}
	}
	return index + 1, nil
}

// ExpectTokens evaluates the restricted sum grammar each problem's `expect`
// field uses. It is an independent oracle: it reads the claim and the bytes,
// never the reference implementation.
func ExpectTokens(spec string, claim []byte, input string) (int, error) {
	collected, err := validateClaim([]byte(input), claim)
	if err != nil {
		return 0, err
	}
	fields, cases := collected.values, collected.caseCount
	total := 0
	for atom := range strings.SplitSeq(spec, "+") {
		atom = strings.TrimSpace(atom)
		switch {
		case atom == "1":
			total++
		case atom == "cases":
			if cases < 0 {
				return 0, fmt.Errorf("cases requires a repeated input field")
			}
			total += cases
		case strings.HasPrefix(atom, "cases."):
			name := strings.TrimPrefix(atom, "cases.")
			bound, ok := fields[name]
			if !ok {
				return 0, fmt.Errorf("expect references unbound field %s", text.Quote(name))
			}
			for _, value := range bound {
				number, err := strconv.Atoi(value)
				if err != nil {
					return 0, fmt.Errorf("field %s is not an integer", text.Quote(name))
				}
				total += number
			}
		default:
			return 0, fmt.Errorf("invalid expect atom %s", text.Quote(atom))
		}
	}
	return total, nil
}
