package grpcserver

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	dbv1 "github.com/example/db-service/internal/platform/genproto/db/v1"
)

// ListAppSettings returns every global runtime override.
func (s *Server) ListAppSettings(
	ctx context.Context, _ *dbv1.ListAppSettingsRequest,
) (*dbv1.ListAppSettingsResponse, error) {
	rows, err := s.svc.ListAppSettings(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*dbv1.AppSetting, 0, len(rows))
	for _, r := range rows {
		out = append(out, &dbv1.AppSetting{Key: r.Key, Value: r.Value})
	}
	return &dbv1.ListAppSettingsResponse{Settings: out}, nil
}

// SetAppSetting upserts a global runtime override.
func (s *Server) SetAppSetting(
	ctx context.Context, req *dbv1.SetAppSettingRequest,
) (*dbv1.SetAppSettingResponse, error) {
	st := req.GetSetting()
	if st == nil || st.GetKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "setting.key is required")
	}
	if err := s.svc.SetAppSetting(ctx, st.GetKey(), st.GetValue()); err != nil {
		return nil, toStatus(err)
	}
	return &dbv1.SetAppSettingResponse{}, nil
}

// DeleteAppSetting removes a global runtime override.
func (s *Server) DeleteAppSetting(
	ctx context.Context, req *dbv1.DeleteAppSettingRequest,
) (*dbv1.DeleteAppSettingResponse, error) {
	if req.GetKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "key is required")
	}
	if err := s.svc.DeleteAppSetting(ctx, req.GetKey()); err != nil {
		return nil, toStatus(err)
	}
	return &dbv1.DeleteAppSettingResponse{}, nil
}
