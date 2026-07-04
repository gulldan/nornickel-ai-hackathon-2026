package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/example/main-service/internal/platform/config"
	"github.com/example/main-service/internal/platform/dbclient"
	"github.com/example/main-service/internal/platform/httpx"
	"github.com/example/main-service/internal/platform/runtimecfg"
	"github.com/example/main-service/internal/platform/valkey"
)

// AppSettingsStore persists global runtime overrides in Postgres (via
// db-service) and mirrors the full set into Valkey, where every service reads
// it with a short cache — settings apply without redeploy.
type AppSettingsStore struct {
	db *dbclient.Client
	kv *valkey.Client
}

// NewAppSettingsStore wires the store over the shared clients.
func NewAppSettingsStore(db *dbclient.Client, kv *valkey.Client) *AppSettingsStore {
	return &AppSettingsStore{db: db, kv: kv}
}

// Republish mirrors the current override set from Postgres into Valkey.
func (s *AppSettingsStore) Republish(ctx context.Context) error {
	vals, err := s.db.ListAppSettings(ctx)
	if err != nil {
		return err
	}
	return runtimecfg.Publish(ctx, s.kv, vals)
}

func (s *AppSettingsStore) apply(ctx context.Context, changes map[string]*string) error {
	for key, val := range changes {
		if val == nil || *val == "" {
			if err := s.db.DeleteAppSetting(ctx, key); err != nil {
				return err
			}
			continue
		}
		if err := s.db.SetAppSetting(ctx, key, *val); err != nil {
			return err
		}
	}
	return s.Republish(ctx)
}

// settingSpec describes one runtime-tunable key: its value kind ("string",
// "bool", "number", "secret"), UI group and the built-in default shown when
// neither an override nor the env variable is set.
type settingSpec struct {
	Key   string `json:"key"`
	Kind  string `json:"kind"`
	Group string `json:"group"`
	Def   string `json:"default"`
}

const (
	kindString = "string"
	kindBool   = "bool"
	kindNumber = "number"
	kindSecret = "secret"

	groupLLM        = "llm"
	groupPubsearch  = "pubsearch"
	groupGeneration = "generation"

	boolTrue = "true"
)

func appSettingSpecs() []settingSpec {
	return []settingSpec{
		{Key: "VLLM_URL", Kind: kindString, Group: groupLLM, Def: ""},
		{Key: "VLLM_MODEL", Kind: kindString, Group: groupLLM, Def: ""},
		{Key: "VLLM_API_KEY", Kind: kindSecret, Group: groupLLM, Def: ""},
		{Key: "RAG_REASONER_MODEL", Kind: kindString, Group: groupLLM, Def: ""},
		{Key: "LLM_RUB_PER_USD", Kind: kindNumber, Group: groupLLM, Def: "90"},
		{Key: "LLM_COST_CURRENCY", Kind: kindString, Group: groupLLM, Def: "₽"},
		{Key: "LLM_PRICES", Kind: kindString, Group: groupLLM, Def: ""},
		{Key: "PUBSEARCH_ENABLED", Kind: kindBool, Group: groupPubsearch, Def: boolTrue},
		{Key: "PUBSEARCH_MAILTO", Kind: kindString, Group: groupPubsearch, Def: ""},
		{Key: "PUBSEARCH_RECENT_YEARS", Kind: kindNumber, Group: groupPubsearch, Def: "5"},
		{Key: "RAG_GEN_BREADTH", Kind: kindBool, Group: groupGeneration, Def: boolTrue},
		{Key: "RAG_GEN_QUALITY_FLOOR", Kind: kindNumber, Group: groupGeneration, Def: "0.35"},
		{Key: "KPI_SUGGEST", Kind: kindBool, Group: groupGeneration, Def: boolTrue},
	}
}

func specFor(key string) (settingSpec, bool) {
	for _, s := range appSettingSpecs() {
		if s.Key == key {
			return s, true
		}
	}
	return settingSpec{}, false
}

func validSettingValue(spec settingSpec, v string) bool {
	switch spec.Kind {
	case kindBool:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "t", boolTrue, "yes", "on", "0", "f", "false", "no", "off":
			return true
		}
		return false
	case kindNumber:
		_, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return err == nil
	default:
		return true
	}
}

type appSettingView struct {
	settingSpec
	Override    string `json:"override"`
	HasOverride bool   `json:"hasOverride"`
	EnvValue    string `json:"envValue"`
	EnvSet      bool   `json:"envSet"`
	Source      string `json:"source"`
}

func (s *AppSettingsStore) view(ctx context.Context) ([]appSettingView, error) {
	overrides, err := s.db.ListAppSettings(ctx)
	if err != nil {
		return nil, err
	}
	specs := appSettingSpecs()
	out := make([]appSettingView, 0, len(specs))
	for _, spec := range specs {
		ov, hasOv := overrides[spec.Key]
		env := config.Get(spec.Key, "")
		v := appSettingView{
			settingSpec: spec,
			Override:    ov,
			HasOverride: hasOv,
			EnvValue:    env,
			EnvSet:      env != "",
			Source:      "default",
		}
		switch {
		case hasOv:
			v.Source = "db"
		case env != "":
			v.Source = "env"
		}
		if spec.Kind == kindSecret {
			v.Override = ""
			v.EnvValue = ""
		}
		out = append(out, v)
	}
	return out, nil
}

// getAppSettings returns the runtime-tunable settings with their effective
// sources. Admin only; secret values are never echoed back.
func (a *API) getAppSettings(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "admin") {
		return
	}
	if a.appSettings == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "settings store is not configured")
		return
	}
	views, err := a.appSettings.view(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "list settings: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"settings": views})
}

// putAppSettings applies a batch of override changes: empty or null value
// removes the override (falling back to env/default), anything else sets it.
func (a *API) putAppSettings(w http.ResponseWriter, r *http.Request) {
	if !a.requireRole(w, r, "admin") {
		return
	}
	if a.appSettings == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "settings store is not configured")
		return
	}
	var body struct {
		Values map[string]*string `json:"values"`
	}
	if err := decodeResource(r, &body); err != nil || len(body.Values) == 0 {
		httpx.Error(w, http.StatusBadRequest, "values object is required")
		return
	}
	for key, val := range body.Values {
		spec, known := specFor(key)
		if !known {
			httpx.Error(w, http.StatusBadRequest, "unknown setting: "+key)
			return
		}
		if val != nil && *val != "" && !validSettingValue(spec, *val) {
			httpx.Error(w, http.StatusBadRequest, "invalid value for "+key)
			return
		}
	}
	if err := a.appSettings.apply(r.Context(), body.Values); err != nil {
		httpx.Error(w, http.StatusBadGateway, "save settings: "+err.Error())
		return
	}
	views, err := a.appSettings.view(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "list settings: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"settings": views})
}
