package application

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/example/main-service/internal/domain"
	"github.com/example/main-service/internal/platform/logger"

	commonv1 "github.com/example/main-service/internal/platform/genproto/common/v1"
)

// defaultTopK is the base number of chunks the RAG pipeline retrieves per chat
// turn; adaptiveTopK widens it (up to chatTopKMax) for short, broad questions.
// llm-service reranks, so deeper recall sharpens the kept context.
const (
	defaultTopK int32 = 8
	chatTopKMax int32 = 12

	historyQuestionsMax = 3
	historyQuestionClip = 240
	historyAnswerClip   = 400
)

var citationLabelRe = regexp.MustCompile(`\s*\[S\d+\]`)

// ChatService owns the chat read paths and the "ask" use case, which persists
// the user turn, calls the LLM service for a grounded answer, and persists the
// assistant turn with its citations.
type ChatService struct {
	chats domain.ChatCatalog
	llm   domain.Answerer
}

// NewChatService wires the chat dependencies.
func NewChatService(chats domain.ChatCatalog, llm domain.Answerer) *ChatService {
	return &ChatService{chats: chats, llm: llm}
}

// CreateChat starts a new chat owned by ownerID; source marks the page it
// started from ("search").
func (s *ChatService) CreateChat(ctx context.Context, ownerID, title, source string) (*commonv1.Chat, error) {
	return s.chats.CreateChat(ctx, ownerID, title, source)
}

// ListChats returns a page of the owner's chats (or all users' for the admin
// history view) plus the total count.
func (s *ChatService) ListChats(
	ctx context.Context, ownerID string, limit, offset int, allOwners bool,
) ([]*commonv1.Chat, int64, error) {
	return s.chats.ListChats(ctx, ownerID, limit, offset, allOwners)
}

// GetChat returns a chat owned by ownerID, or ErrForbidden if owned by someone else.
func (s *ChatService) GetChat(ctx context.Context, ownerID, chatID string) (*commonv1.Chat, error) {
	chat, err := s.chats.GetChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	if chat.GetOwnerId() != ownerID {
		return nil, ErrForbidden
	}
	return chat, nil
}

// ListMessages returns the messages of a chat owned by ownerID.
func (s *ChatService) ListMessages(ctx context.Context, ownerID, chatID string) ([]*commonv1.Message, error) {
	if _, err := s.GetChat(ctx, ownerID, chatID); err != nil {
		return nil, err
	}
	return s.chats.ListMessages(ctx, chatID)
}

// ListModels returns the AI model catalogue from db-service.
func (s *ChatService) ListModels(ctx context.Context) ([]*commonv1.Model, error) {
	return s.chats.ListModels(ctx)
}

// Ask runs one chat turn: verify ownership, persist the user message, ask the
// LLM service for a grounded answer, then persist and return the assistant
// message (carrying its source citations).
func (s *ChatService) Ask(ctx context.Context, cmd domain.AskCommand) (*commonv1.Message, error) {
	ctx, cancel := withDeadline(ctx, llmSinglePassTimeout)
	defer cancel()
	log := logger.From(ctx)

	// Ownership check first so we never write into someone else's chat.
	if _, err := s.GetChat(ctx, cmd.OwnerID, cmd.ChatID); err != nil {
		return nil, err
	}

	history, histErr := s.chats.ListMessages(ctx, cmd.ChatID)
	if histErr != nil {
		log.Warn().Err(histErr).Str("chat_id", cmd.ChatID).Msg("history unavailable; asking without dialog context")
		history = nil
	}

	if _, err := s.chats.AddMessage(ctx, cmd.ChatID, "user", cmd.Content, nil, ""); err != nil {
		return nil, fmt.Errorf("persist user message: %w", err)
	}

	resp, err := s.llm.Answer(ctx, &commonv1.RagRequest{
		OwnerId: cmd.OwnerID,
		Query:   contextualQuery(history, cmd.Content),
		TopK:    adaptiveTopK(cmd.Content, defaultTopK, chatTopKMax),
		Stream:  false,
	})
	if err != nil {
		return nil, fmt.Errorf("rag answer: %w", err)
	}

	assistant, err := s.chats.AddMessage(ctx, cmd.ChatID, "assistant", resp.GetAnswer(), resp.GetSources(), answerMeta(resp))
	if err != nil {
		return nil, fmt.Errorf("persist assistant message: %w", err)
	}
	log.Info().
		Str("chat_id", cmd.ChatID).
		Int("sources", len(resp.GetSources())).
		Bool("cached", resp.GetCached()).
		Msg("chat turn answered")
	return assistant, nil
}

func contextualQuery(history []*commonv1.Message, question string) string {
	questions, lastAnswer := dialogContext(history)
	if len(questions) == 0 && lastAnswer == "" {
		return question
	}
	var b strings.Builder
	b.WriteString(question)
	b.WriteString("\n\nКонтекст диалога для понимания вопроса:")
	for _, q := range questions {
		b.WriteString("\nРанее спрашивали: ")
		b.WriteString(q)
	}
	if lastAnswer != "" {
		b.WriteString("\nПоследний ответ (начало): ")
		b.WriteString(lastAnswer)
	}
	return b.String()
}

func dialogContext(history []*commonv1.Message) ([]string, string) {
	questions := make([]string, 0, historyQuestionsMax)
	lastAnswer := ""
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		text := strings.TrimSpace(m.GetContent())
		if text == "" {
			continue
		}
		switch m.GetRole() {
		case "user":
			if len(questions) < historyQuestionsMax {
				q := strings.TrimSpace(strings.ReplaceAll(text, "?", " "))
				questions = append(questions, truncateRunes(q, historyQuestionClip))
			}
		case "assistant":
			if lastAnswer == "" {
				clean := strings.Join(strings.Fields(citationLabelRe.ReplaceAllString(text, " ")), " ")
				lastAnswer = truncateRunes(clean, historyAnswerClip)
			}
		}
		if len(questions) == historyQuestionsMax && lastAnswer != "" {
			break
		}
	}
	slices.Reverse(questions)
	return questions, lastAnswer
}

// answerMeta renders the provenance envelope stored with an assistant message:
// {"model":…,"cached":…,"trace":{…}}. The trace is protojson (snake_case, per
// the proto contract); readers should unmarshal it with DiscardUnknown so the
// envelope survives future trace fields. Best-effort: "" on a marshal failure.
func answerMeta(resp *commonv1.RagResponse) string {
	env := struct {
		Model  string          `json:"model"`
		Cached bool            `json:"cached"`
		Trace  json.RawMessage `json:"trace,omitempty"`
	}{Model: resp.GetModel(), Cached: resp.GetCached()}
	if tr := resp.GetTrace(); tr != nil {
		if raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(tr); err == nil {
			env.Trace = raw
		}
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return ""
	}
	return string(raw)
}
