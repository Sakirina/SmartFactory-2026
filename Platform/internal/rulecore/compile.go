package rulecore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"

	"competition2026/product/platform/pkg/model"
	"competition2026/product/platform/pkg/precise"
)

func digest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func ContentHash(definition model.Definition) (string, error) {
	definition.ExecutionPlan = nil
	return digest(definition)
}

func PlanID(definition model.Definition, content string) string {
	return definition.ID + ":" + strconv.FormatInt(definition.Version, 10) + ":" + content
}

func clone[T any](value T) (T, error) {
	var result T
	data, err := json.Marshal(value)
	if err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	err = decoder.Decode(&result)
	return result, err
}

func Verify(plan *model.ExecutionPlan, definition model.Definition) error {
	if plan == nil || plan.Format != model.ExecutionPlanFormat {
		return errors.New("missing or unsupported execution plan format")
	}
	content, err := ContentHash(definition)
	if err != nil {
		return err
	}
	if plan.ContentSHA256 != content || plan.ID != PlanID(definition, content) || plan.DefinitionID != definition.ID || plan.DefinitionVersion != definition.Version {
		return errors.New("execution plan does not match the definition content or version")
	}
	copy := *plan
	copy.SHA256 = ""
	sum, err := digest(copy)
	if err != nil {
		return err
	}
	if sum != plan.SHA256 {
		return errors.New("execution plan checksum mismatch")
	}
	if len(plan.Nodes) < 1 || len(plan.Nodes) > 128 {
		return errors.New("invalid execution plan node count")
	}
	return nil
}

// Compile checks authoring metadata and emits an immutable, serializable plan.
// Dependency availability is supplied by the authenticated preparation layer.
func Compile(ctx context.Context, definition model.Definition) (*model.ExecutionPlan, model.Validation) {
	compilations.Add(1)
	validation := model.Validation{Valid: true, Errors: []string{}, Order: []string{}}
	add := func(message string) { validation.Valid = false; validation.Errors = append(validation.Errors, message) }
	definition.ExecutionPlan = nil
	definition, err := clone(definition)
	if err != nil {
		add("definition JSON: " + err.Error())
		return nil, validation
	}
	if definition.ID == "" || definition.Name == "" {
		add("id and name are required")
	}
	if !contains([]string{"analysis", "alarm", "strategy"}, definition.Kind) {
		add("kind must be analysis, alarm or strategy")
	}
	if definition.SchemaVersion != model.ContractVersion {
		add("unsupported schema_version")
	}
	if definition.GroupID == "" {
		add("group_id is required")
	}
	if len(definition.Nodes) < 1 || len(definition.Nodes) > 128 {
		add("definition must have 1 to 128 nodes")
	}
	if len(definition.Connections) > 512 {
		add("definition exceeds 512 connections")
	}
	catalog := map[string]model.NodeMetadata{}
	for _, item := range model.NodeCatalog() {
		catalog[item.Type] = item
	}
	nodes := map[string]model.Node{}
	compiled := map[string]model.ExecutionNode{}
	incoming := map[string]int{}
	outgoing := map[string][]string{}
	for _, node := range definition.Nodes {
		if err := ctx.Err(); err != nil {
			add(err.Error())
			return nil, validation
		}
		if node.ID == "" {
			add("node id is required")
		}
		if _, exists := nodes[node.ID]; exists {
			add("duplicate node " + node.ID)
		}
		nodes[node.ID] = node
		metadata, ok := catalog[node.Type]
		if !ok || !contains(metadata.Kinds, definition.Kind) {
			add("node " + node.ID + " is not allowed in " + definition.Kind)
			continue
		}
		params, issues := parameters(node, metadata)
		for _, issue := range issues {
			add(issue)
		}
		prepared := model.ExecutionNode{ID: node.ID, Type: node.Type, Params: params, Inputs: []model.Connection{}}
		if node.Type == "expression" {
			code, _ := node.Params["code"].(string)
			prepared.Expression, err = CompileExpression(code)
			if err == nil {
				err = ruleVariables(prepared.Expression)
			}
			if err == nil {
				_, err = expressionType(prepared.Expression)
			}
			if err != nil {
				add("node " + node.ID + " params.code: " + err.Error())
			}
		}
		compiled[node.ID] = prepared
	}
	for _, connection := range definition.Connections {
		from, fromOK := nodes[connection.From]
		to, toOK := nodes[connection.To]
		if !fromOK || !toOK {
			add("connection references a missing node")
			continue
		}
		fromPorts, toPorts := from.Outputs, to.Inputs
		if len(fromPorts) == 0 {
			fromPorts = catalog[from.Type].Outputs
		}
		if len(toPorts) == 0 {
			toPorts = catalog[to.Type].Inputs
		}
		ft, foundF := portType(fromPorts, connection.FromPort)
		tt, foundT := portType(toPorts, connection.ToPort)
		if !foundF || !foundT {
			add("connection " + connection.From + " -> " + connection.To + " references a missing port")
		} else if ft != "any" && tt != "any" && ft != tt && !(ft == "integer" && tt == "number") {
			add("port type mismatch " + connection.From + " -> " + connection.To)
		}
		incoming[connection.To]++
		outgoing[connection.From] = append(outgoing[connection.From], connection.To)
		prepared := compiled[connection.To]
		prepared.Inputs = append(prepared.Inputs, connection)
		compiled[connection.To] = prepared
		if connection.FromPort == "error" {
			prepared := compiled[connection.From]
			prepared.ErrorOutput = true
			compiled[connection.From] = prepared
		}
	}
	ready := []string{}
	for id := range nodes {
		if incoming[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		validation.Order = append(validation.Order, id)
		for _, next := range outgoing[id] {
			incoming[next]--
			if incoming[next] == 0 {
				ready = append(ready, next)
				sort.Strings(ready)
			}
		}
	}
	if len(validation.Order) != len(nodes) {
		add("graph contains a cycle")
	}
	for _, output := range definition.Outputs {
		if _, ok := nodes[output.NodeID]; !ok {
			add("output references a missing node")
		}
		if output.Key == "" {
			add("output key is required")
		}
	}
	validatePolicy(definition, add)
	if !validation.Valid {
		return nil, validation
	}
	content, err := ContentHash(definition)
	if err != nil {
		add(err.Error())
		return nil, validation
	}
	plan := &model.ExecutionPlan{ID: PlanID(definition, content), Format: model.ExecutionPlanFormat, DefinitionID: definition.ID, DefinitionVersion: definition.Version, ContentSHA256: content, Kind: definition.Kind, Selector: definition.Selector, Policy: definition.Policy, Outputs: definition.Outputs, Dependencies: definition.Dependencies, Nodes: []model.ExecutionNode{}}
	for _, id := range validation.Order {
		plan.Nodes = append(plan.Nodes, compiled[id])
	}
	plan.SHA256, err = digest(plan)
	if err != nil {
		add(err.Error())
		return nil, validation
	}
	return plan, validation
}

func portType(ports []model.Port, name string) (string, bool) {
	if name == "" {
		name = "value"
	}
	for _, port := range ports {
		if port.Name == name || (port.Name == "" && name == "value") {
			return port.Type, true
		}
	}
	return "", false
}

func parameters(node model.Node, metadata model.NodeMetadata) (model.NodeParameters, []string) {
	values := map[string]any{}
	issues := []string{}
	for _, field := range metadata.Parameters {
		value, supplied := node.Params[field.Key]
		if (!supplied || value == "") && field.Default != nil {
			value, supplied = field.Default, true
		}
		issue := ""
		if !supplied {
			if field.Required {
				issue = "is required"
			}
		} else {
			switch field.Type {
			case "string":
				text, ok := value.(string)
				if !ok || field.Required && text == "" {
					issue = "must be a nonempty string"
				} else if len(field.Enum) > 0 && !contains(field.Enum, text) {
					issue = "has an unsupported value"
				}
			case "number", "integer":
				number, ok := precise.Number(value)
				if !ok || field.Type == "integer" && (!number.IsInt() || !number.Num().IsInt64()) {
					issue = "must be a supported " + field.Type
				} else if field.Minimum != nil && number.Cmp(big.NewRat(*field.Minimum, 1)) < 0 || field.Maximum != nil && number.Cmp(big.NewRat(*field.Maximum, 1)) > 0 {
					issue = "is outside the permitted range"
				}
			}
		}
		if issue != "" {
			issues = append(issues, "node "+node.ID+" params."+field.Key+" "+issue)
		}
		values[field.Key] = value
	}
	str := func(key string) string { value, _ := values[key].(string); return value }
	params := model.NodeParameters{Key: str("key"), Function: str("function"), Operator: str("operator"), Mode: str("mode"), Direction: str("direction"), Severity: str("severity"), Value: values["value"], High: values["high"], Low: values["low"]}
	if duration, ok := precise.Number(values["duration_ms"]); ok && duration.IsInt() && duration.Num().IsInt64() {
		params.DurationMS = duration.Num().Int64()
	}
	if node.Type == "hysteresis" {
		hi, highOK := precise.Number(params.High)
		lo, lowOK := precise.Number(params.Low)
		if highOK && lowOK && lo.Cmp(hi) > 0 {
			issues = append(issues, "node "+node.ID+" params.low must be <= params.high")
		}
	}
	return params, issues
}

func ruleVariables(tree *model.ExpressionTree) error {
	if tree == nil {
		return nil
	}
	if tree.Op == "variable" && !contains([]string{"value", "sum", "count", "min", "max", "avg", "previous", "active", "good", "quality", "fresh"}, tree.Name) {
		return fmt.Errorf("unknown variable %s", tree.Name)
	}
	for _, child := range tree.Args {
		if err := ruleVariables(child); err != nil {
			return err
		}
	}
	return nil
}

func validatePolicy(definition model.Definition, add func(string)) {
	policy := definition.Policy
	if policy.TimeoutMS > 1000 || policy.TimeoutMS < 0 {
		add("script timeout must be 1..1000 ms")
	}
	if definition.Kind != "strategy" {
		return
	}
	if policy.FreshnessMS < 0 || policy.FreshnessMS > 86400000 {
		add("freshness_ms must be 0..86400000")
	}
	if policy.Watchdog && (len(definition.Selector.DeviceIDs) == 0 || len(definition.Selector.Keys) == 0) {
		add("watchdog requires explicit device and field selectors")
	}
	if len(policy.Steps) == 0 {
		add("strategy requires at least one device step")
	}
	if len(policy.EdgeIDs) > 1 && len(policy.Degraded) == 0 {
		add("cross-edge strategy requires degraded steps")
	}
	steps := map[string]bool{}
	for _, step := range append(append([]model.Step{}, policy.Steps...), policy.Degraded...) {
		if step.ID == "" || step.DeviceID == "" || step.Action == "" || step.EdgeID == "" {
			add("step requires id, edge_id, device_id and action")
		}
		if steps[step.ID] {
			add("step IDs must be unique")
		}
		steps[step.ID] = true
	}
	if schedule := policy.Schedule; schedule != nil && (schedule.EveryMS < 1000 || schedule.WindowMS <= 0 || schedule.Timezone != "Asia/Shanghai") {
		add("schedule requires interval >= 1000 ms, execution window and Asia/Shanghai timezone")
	}
}
