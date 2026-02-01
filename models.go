package main

//================================================================
// データ構造体 (Structs)
//================================================================

// --- C++実行用 ---

// /execute へのリクエストボディ
type CodePayload struct {
	Code  string `json:"code"`
	Stdin string `json:"stdin"`
}

// /execute からのレスポンスボディ
type ResultPayload struct {
	Result string `json:"result"`
}

// --- AIチャット用 ---

// /api/chat へのリクエストボディ
type ChatPayload struct {
	Message     string `json:"message"`
	Code        string `json:"code"`
	Task        string `json:"task"`
	LoveLevel   int    `json:"love_level"`
	CharacterID string `json:"character_id"`
	UserID      string `json:"user_id"`
	PrevParams  struct {
		Joy      int `json:"joy"`
		Trust    int `json:"trust"`
		Fear     int `json:"fear"`
		Anger    int `json:"anger"`
		Shy      int `json:"shy"`
		Surprise int `json:"surprise"`
	} `json:"prev_params"`
	PrevOutput string `json:"prev_output"`
}

// /api/chat からのレスポンスボディ
type ChatResponse struct {
	Thought    string   `json:"thought"` // 思考プロセス
	Parameters struct { // 感情パラメータ
		Joy      int `json:"joy"`
		Trust    int `json:"trust"`
		Fear     int `json:"fear"`
		Anger    int `json:"anger"`
		Shy      int `json:"shy"`
		Surprise int `json:"surprise"`
	} `json:"parameters"`
	Text    string `json:"text"`
	Emotion string `json:"emotion"`
	LoveUp  int    `json:"love_up"`
}

type ResponseFormat struct {
	Type string `json:"type"`
}

// OpenAI API へのリクエストボディ
type OpenAIRequest struct {
	Model          string          `json:"model"`
	Messages       []OpenAIMessage `json:"messages"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

// OpenAI API で使用するメッセージ構造体
type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenAI API からのレスポンスボディ
type OpenAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// 記憶データ構造
type UserMemory struct {
	Summary       string   `json:"summary"`        // ユーザーの学習状況・特徴の要約
	LearnedTopics []string `json:"learned_topics"` // 学んだ項目リスト
	Weaknesses    []string `json:"weaknesses"`     // 苦手な項目リスト
	LoveLevel     int      `json:"love_level"`     // (オプション) 親密度をサーバー側でもバックアップしたい場合
	LastUpdated   string   `json:"last_updated"`   // 最終更新日時
}

// 要約リクエストの構造体
type SummarizeRequest struct {
	UserID  string `json:"user_id"`
	ChatLog []struct {
		Username string `json:"username"`
		Message  string `json:"message"`
	} `json:"chat_history"`
}

// 採点リクエスト用
type GradePayload struct {
	UserID         string `json:"user_id"`
	TaskID         string `json:"task_id"`
	Code           string `json:"code"`            // ユーザーのコード
	Output         string `json:"output"`          // 実行結果の出力
	TaskDesc       string `json:"task_desc"`       // 課題文
	ExpectedOutput string `json:"expected_output"` // 想定出力
}

// 採点レスポンス用 (AIからのJSONをマッピング)
type GradeResponse struct {
	Score       int    `json:"score"`
	Reason      string `json:"reason"`
	Improvement string `json:"improvement"`
}

// Supabase採点用の構造体
type UserTaskProgress struct {
	UserID    string `json:"user_id"`
	TaskID    string `json:"task_id"`
	HighScore int    `json:"high_score"`
	IsCleared bool   `json:"is_cleared"`
}

// DBの profiles テーブル用構造体
type UserProfile struct {
	ID            string   `json:"id"`
	LoveLevel     int      `json:"love_level"`
	Summary       string   `json:"summary"`
	LearnedTopics []string `json:"learned_topics"`
	Weaknesses    []string `json:"weaknesses"`
	LastUpdated   string   `json:"last_updated"`
	Role          string   `json:"role"`
	Name          string   `json:"name"`
}

// トークモード用
type TalkRequest struct {
	UserID    string        `json:"user_id"`
	Message   string        `json:"message"` // ユーザーの入力
	History   []ChatMessage `json:"history"` // 会話履歴
	Mode      string        `json:"mode"`    // "chat" or "quiz"
	LoveLevel int           `json:"love_level"`
	QuizCount int           `json:"quiz_count"`
}

// 会話履歴の要素
type ChatMessage struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// フロントエンドへのレスポンス (JSONシナリオ)
type TalkResponse struct {
	Thought    string   `json:"thought"` // 思考プロセス
	Parameters struct { // 感情パラメータ
		Joy      int `json:"joy"`
		Trust    int `json:"trust"`
		Fear     int `json:"fear"`
		Anger    int `json:"anger"`
		Shy      int `json:"shy"`
		Surprise int `json:"surprise"`
	} `json:"parameters"`
	Script     []ScriptAction `json:"script"`
	EndSession bool           `json:"end_session,omitempty"`
}

// シナリオの1アクション
type ScriptAction struct {
	Type    string   `json:"type"`              // "text", "emotion", "choices"
	Content string   `json:"content,omitempty"` // セリフ または 表情ID
	Choices []Choice `json:"choices,omitempty"` // 選択肢リスト
}

type Choice struct {
	Label string `json:"label"` // ボタンの表示名
	Value string `json:"value"` // 送信する値
}
