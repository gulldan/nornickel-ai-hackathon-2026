package grpcserver

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/example/db-service/internal/domain"
	dbv1 "github.com/example/db-service/internal/platform/genproto/db/v1"
)

// dayLayout is the wire format for llm_usage_daily.day ("YYYY-MM-DD").
const dayLayout = "2006-01-02"

// SetLLMUsageDaily mirrors a running daily usage total into the ledger.
func (s *Server) SetLLMUsageDaily(
	ctx context.Context, req *dbv1.SetLLMUsageDailyRequest,
) (*dbv1.SetLLMUsageDailyResponse, error) {
	r := req.GetRow()
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "row is required")
	}
	day, err := time.Parse(dayLayout, r.GetDay())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "day must be YYYY-MM-DD")
	}
	if r.GetModel() == "" || r.GetOperation() == "" {
		return nil, status.Error(codes.InvalidArgument, "model and operation are required")
	}
	err = s.svc.SetLLMUsage(ctx, &domain.LLMUsageDaily{
		Day:              day,
		Model:            r.GetModel(),
		Operation:        r.GetOperation(),
		Requests:         r.GetRequests(),
		PromptTokens:     r.GetPromptTokens(),
		CompletionTokens: r.GetCompletionTokens(),
		CostNanoUSD:      r.GetCostNanoUsd(),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return &dbv1.SetLLMUsageDailyResponse{}, nil
}

// ListLLMUsageDaily returns the ledger for an inclusive [from_day, to_day] range.
func (s *Server) ListLLMUsageDaily(
	ctx context.Context, req *dbv1.ListLLMUsageDailyRequest,
) (*dbv1.ListLLMUsageDailyResponse, error) {
	from, err := time.Parse(dayLayout, req.GetFromDay())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "from_day must be YYYY-MM-DD")
	}
	to, err := time.Parse(dayLayout, req.GetToDay())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "to_day must be YYYY-MM-DD")
	}
	rows, err := s.svc.ListLLMUsage(ctx, from, to)
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*dbv1.LLMUsageDailyRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, &dbv1.LLMUsageDailyRow{
			Day:              r.Day.Format(dayLayout),
			Model:            r.Model,
			Operation:        r.Operation,
			Requests:         r.Requests,
			PromptTokens:     r.PromptTokens,
			CompletionTokens: r.CompletionTokens,
			CostNanoUsd:      r.CostNanoUSD,
		})
	}
	return &dbv1.ListLLMUsageDailyResponse{Rows: out}, nil
}
