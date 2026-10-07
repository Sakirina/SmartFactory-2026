package aievidencefixture

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"competition2026/product/platform/internal/api"
	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/businessfixture"
	"competition2026/product/platform/internal/historymodel"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func Seed(ctx context.Context, server *api.Server, fixture *businessfixture.Fixture) (Plan, error) {
	now := time.Now().UnixMilli()
	plan := Plan{DefinitionID: "调查/温度:一号", DeviceID: "device-climate-ventilation", ExecutionID: "执行/通风:一号", RunID: "历史/温度:一号", OversizedID: "调查/超预算:一号", FromMS: now - 1000, ToMS: now + 1000}
	definitions := server.DefinitionApplication()
	definition := model.Definition{ID: plan.DefinitionID, Name: "调查温度分析", Kind: "analysis", SchemaVersion: model.ContractVersion, GroupID: "factory", Selector: model.Selector{DeviceIDs: []string{plan.DeviceID}, Keys: []string{"temperature"}}, Nodes: []model.Node{{ID: "source", Type: "input", Label: "温度采样"}, {ID: "output", Type: "output", Label: "温度输出"}}, Connections: []model.Connection{{From: "source", To: "output"}}, Outputs: []model.Output{{Key: "temperature", Type: "number", Unit: "°C", NodeID: "output"}}}
	publish := func(d model.Definition) error {
		draft, err := definitions.SaveDraft(ctx, fixture.Principal, application.SaveDraftInput{Draft: model.Draft{ID: d.ID, Definition: d}})
		if err != nil {
			return err
		}
		version := draft.Version
		_, err = definitions.PublishDraft(ctx, fixture.Principal, application.DraftInput{ID: draft.ID, ExpectedVersion: &version})
		return err
	}
	if err := publish(definition); err != nil {
		return plan, err
	}
	oversized := definition
	oversized.ID = plan.OversizedID
	oversized.Name = "超预算验证定义"
	oversized.Nodes = append([]model.Node{}, definition.Nodes...)
	oversized.Nodes[0].Label = strings.Repeat("预算内容", 150000)
	if err := publish(oversized); err != nil {
		return plan, err
	}
	strategy := model.Definition{ID: "调查/通风策略:一号", Name: "调查通风预案", Kind: "strategy", SchemaVersion: model.ContractVersion, GroupID: "factory", Selector: model.Selector{DeviceIDs: []string{plan.DeviceID}, Keys: []string{"temperature"}}, Nodes: []model.Node{{ID: "source", Type: "input"}, {ID: "action", Type: "action"}}, Connections: []model.Connection{{From: "source", To: "action"}}, Policy: model.Policy{RiskCategory: "business", RiskLevel: 1, SafetyUserID: fixture.Principal.User.ID, Steps: []model.Step{{ID: "fan-on", DeviceID: plan.DeviceID, EdgeID: server.NodeID, Action: "set_fan", Params: map[string]string{"value": "true"}, Idempotent: true, TimeoutMS: 1000}}}}
	if err := publish(strategy); err != nil {
		return plan, err
	}
	if _, err := server.Control.Create(ctx, fixture.Principal, strategy.ID, map[string]string{}, false, plan.ExecutionID); err != nil {
		return plan, err
	}
	now = time.Now().UnixMilli()
	plan.FromMS, plan.ToMS = now-1000, now+1000
	point := model.Observation{ID: "调查/采样:一号", MessageID: "调查/消息:一号", DeviceID: plan.DeviceID, SourceID: server.NodeID, Key: "temperature", Value: json.Number("9007199254740993.125"), ObservedMS: now, ReceivedMS: now, Quality: "GOOD", EntityRevision: 1}
	if _, err := server.Store.Ingest(ctx, store.IngestBatch{MessageID: point.MessageID, SourceID: server.NodeID, Points: []model.Observation{point}}); err != nil {
		return plan, err
	}
	if err := server.Engine.Process(ctx, point, false, ""); err != nil {
		return plan, err
	}
	history := server.HistoryApplication()
	if _, err := history.Create(ctx, fixture.Principal, historymodel.Request{ID: plan.RunID, Kind: "replay", DefinitionID: plan.DefinitionID, FromMS: plan.FromMS, ToMS: plan.ToMS, Points: []model.Observation{point}}); err != nil {
		return plan, err
	}
	if err := history.Execute(ctx, plan.RunID); err != nil {
		return plan, err
	}
	return plan, nil
}
