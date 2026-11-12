package nodebridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type nftMarkTable struct {
	Family string `json:"family"`
	Table  string `json:"table"`
}

type nftMarkSet struct {
	nftMarkTable
	Name string `json:"name"`
	Elem []any  `json:"elem"`
}

type nftMarkRule struct {
	nftMarkTable
	Chain string           `json:"chain"`
	Expr  []map[string]any `json:"expr"`
}

type markPredicate func(uint32) bool

func selectRedirectMark(ruleset []byte, ownTable string, preferred uint32) (uint32, error) {
	var listing struct {
		Objects []struct {
			Rule *nftMarkRule `json:"rule"`
			Set  *nftMarkSet  `json:"set"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(ruleset, &listing); err != nil {
		return 0, fmt.Errorf("read nftables marks: %w", err)
	}
	if listing.Objects == nil {
		return 0, errors.New("nftables ruleset has no object list")
	}
	sets := make(map[nftMarkTable]map[string][]any)
	for _, object := range listing.Objects {
		if set := object.Set; set != nil {
			if sets[set.nftMarkTable] == nil {
				sets[set.nftMarkTable] = make(map[string][]any)
			}
			sets[set.nftMarkTable][set.Name] = set.Elem
		}
	}
	var predicates []markPredicate
	for _, object := range listing.Objects {
		rule := object.Rule
		if rule == nil || (rule.Family == "inet" && rule.Table == ownTable) {
			continue
		}
		for _, statement := range rule.Expr {
			predicate, err := nftMarkReservation(statement, sets[rule.nftMarkTable])
			if err != nil {
				return 0, fmt.Errorf("inspect mark rule in %s %s %s: %w", rule.Family, rule.Table, rule.Chain, err)
			}
			if predicate != nil {
				predicates = append(predicates, predicate)
			}
		}
	}
	// Step across the full 32-bit space so a mask covering high bits does
	// not make consecutive values all test the same reserved pattern.
	for attempt := uint32(0); attempt < 4096; attempt++ {
		candidate := preferred + attempt*0x9e3779b9
		if candidate != 0 && !markReserved(candidate, predicates) {
			return candidate, nil
		}
	}
	return 0, errors.New("could not find a Gost socket mark outside existing nftables mark rules")
}

func markReserved(candidate uint32, predicates []markPredicate) bool {
	for _, predicate := range predicates {
		if predicate(candidate) {
			return true
		}
	}
	return false
}

func nftMarkReservation(statement map[string]any, sets map[string][]any) (markPredicate, error) {
	if match, ok := statement["match"].(map[string]any); ok && (hasNftMark(match["left"]) || hasNftMark(match["right"])) {
		left := nftMarkExpression(match["left"])
		if left == nil || hasNftMark(match["right"]) {
			return nil, errors.New("unsupported mark comparison")
		}
		switch match["op"] {
		case "==", "in", "!=":
			matches, ok := nftMarkMembership(match["right"], sets)
			if !ok {
				return nil, errors.New("unsupported mark match value")
			}
			return func(mark uint32) bool { return matches(left(mark)) != (match["op"] == "!=") }, nil
		case "<", "<=", ">", ">=":
			right, ok := nftMarkNumber(match["right"])
			if !ok {
				return nil, errors.New("unsupported ordered mark comparison")
			}
			return func(mark uint32) bool {
				value := left(mark)
				switch match["op"] {
				case "<":
					return value < right
				case "<=":
					return value <= right
				case ">":
					return value > right
				default:
					return value >= right
				}
			}, nil
		default:
			return nil, errors.New("unsupported mark comparison operator")
		}
	}
	if mangle, ok := statement["mangle"].(map[string]any); ok && isNftMark(mangle["key"]) {
		value := nftMarkExpression(mangle["value"])
		if value == nil {
			return nil, errors.New("unsupported mark assignment")
		}
		// Bits set independently of the previous mark are reserved by the rule.
		mask := ^(value(0) ^ value(math.MaxUint32))
		if mask != 0 {
			expected := value(0) & mask
			return func(mark uint32) bool { return mark&mask == expected }, nil
		}
	}
	return nil, nil
}

func nftMarkMembership(expression any, sets map[string][]any) (markPredicate, bool) {
	if value, ok := nftMarkNumber(expression); ok {
		return func(mark uint32) bool { return mark == value }, true
	}
	var elements []any
	switch expression := expression.(type) {
	case string:
		name, ok := strings.CutPrefix(expression, "@")
		if !ok {
			return nil, false
		}
		elements, ok = sets[name]
		if !ok {
			return nil, false
		}
	case map[string]any:
		if bounds, ok := expression["range"].([]any); ok && len(bounds) == 2 {
			first, start := nftMarkNumber(bounds[0])
			last, end := nftMarkNumber(bounds[1])
			return func(mark uint32) bool { return first <= mark && mark <= last }, start && end
		}
		var ok bool
		elements, ok = expression["set"].([]any)
		if !ok {
			return nil, false
		}
	default:
		return nil, false
	}
	var predicates []markPredicate
	for _, element := range elements {
		// Named sets may wrap elements that carry a timeout or a comment.
		if wrapper, ok := element.(map[string]any); ok {
			if inner, ok := wrapper["elem"].(map[string]any); ok {
				element = inner["val"]
			}
		}
		predicate, ok := nftMarkMembership(element, nil)
		if !ok {
			return nil, false
		}
		predicates = append(predicates, predicate)
	}
	return func(mark uint32) bool { return markReserved(mark, predicates) }, true
}

func isNftMark(expression any) bool {
	object, _ := expression.(map[string]any)
	for _, kind := range []string{"meta", "ct"} {
		source, _ := object[kind].(map[string]any)
		if source["key"] == "mark" {
			return true
		}
	}
	return false
}

func hasNftMark(expression any) bool {
	if isNftMark(expression) {
		return true
	}
	switch expression := expression.(type) {
	case []any:
		for _, value := range expression {
			if hasNftMark(value) {
				return true
			}
		}
	case map[string]any:
		for _, value := range expression {
			if hasNftMark(value) {
				return true
			}
		}
	}
	return false
}

func nftMarkExpression(expression any) func(uint32) uint32 {
	if isNftMark(expression) {
		return func(mark uint32) uint32 { return mark }
	}
	if number, ok := nftMarkNumber(expression); ok {
		return func(uint32) uint32 { return number }
	}
	object, ok := expression.(map[string]any)
	if !ok || len(object) != 1 {
		return nil
	}
	for operator, value := range object {
		operands, ok := value.([]any)
		if !ok || len(operands) != 2 {
			return nil
		}
		left, right := nftMarkExpression(operands[0]), nftMarkExpression(operands[1])
		if left == nil || right == nil {
			return nil
		}
		switch operator {
		case "&":
			return func(mark uint32) uint32 { return left(mark) & right(mark) }
		case "|":
			return func(mark uint32) uint32 { return left(mark) | right(mark) }
		case "^":
			return func(mark uint32) uint32 { return left(mark) ^ right(mark) }
		case "<<":
			return func(mark uint32) uint32 { return left(mark) << right(mark) }
		case ">>":
			return func(mark uint32) uint32 { return left(mark) >> right(mark) }
		}
	}
	return nil
}

func nftMarkNumber(value any) (uint32, bool) {
	switch number := value.(type) {
	case float64:
		if number >= 0 && number <= math.MaxUint32 && number == math.Trunc(number) {
			return uint32(number), true
		}
	case string:
		parsed, err := strconv.ParseUint(number, 0, 32)
		return uint32(parsed), err == nil
	}
	return 0, false
}
