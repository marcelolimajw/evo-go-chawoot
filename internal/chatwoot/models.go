package chatwoot

type ContactPayload struct {
	Name        string `json:"name"`
	PhoneNumber string `json:"phone_number,omitempty"`
	Identifier  string `json:"identifier,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	InboxID     int    `json:"inbox_id,omitempty"`
}

type ContactResponse struct {
	Payload struct {
		Contact struct {
			ID int `json:"id"`
		} `json:"contact"`
	} `json:"payload"`
}

type ConversationPayload struct {
	SourceID  string `json:"source_id"`
	InboxID   int    `json:"inbox_id"`
	ContactID int    `json:"contact_id"`
	Status    string `json:"status,omitempty"`
}

type ConversationResponse struct {
	ID int `json:"id"`
}

type MessagePayload struct {
	Content     string `json:"content"`
	MessageType       string                 `json:"message_type"`
	Private           bool                   `json:"private"`
	ExternalID        string                 `json:"external_id,omitempty"`
	SourceID          string                 `json:"source_id,omitempty"`
	ContentAttributes map[string]interface{} `json:"content_attributes,omitempty"`
}

type WebhookPayload struct {
	Event             string `json:"event"`
	InboxID           int    `json:"inbox_id"`
	MessageType       string `json:"message_type"`
	Content           string `json:"content"`
	ID                int    `json:"id"`          // ID da mensagem na raiz
	ExternalID        string `json:"external_id"` // ExternalID na raiz
	SourceID          string `json:"source_id"`   // SourceID na raiz (muitas vezes o external_id)
	Private           bool   `json:"private"`
	ContentAttributes struct {
		Deleted   bool `json:"deleted"`
		InReplyTo int  `json:"in_reply_to"`
	} `json:"content_attributes"`

	Attachments []Attachment `json:"attachments"`

	Sender struct {
		Identifier string `json:"identifier"`
		ID         int    `json:"id"`
		Type       string `json:"type"`
		Name       string `json:"name"`
	} `json:"sender"`

	Message struct {
		ID                int    `json:"id"`
		InboxID           int    `json:"inbox_id"`
		Content           string `json:"content"`
		ExternalID        string `json:"external_id"`
		SourceID          string `json:"source_id"`
		MessageType       string `json:"message_type"`
		ConversationID    int    `json:"conversation_id"`
		Private           bool   `json:"private"`
		ContentAttributes struct {
			Deleted   bool `json:"deleted"`
			InReplyTo int  `json:"in_reply_to"`
		} `json:"content_attributes"`
		Sender struct {
			ID         int    `json:"id"`
			Type       string `json:"type"`
			Identifier string `json:"identifier"`
			Name       string `json:"name"`
		} `json:"sender"`
		Attachments []Attachment `json:"attachments"`
	} `json:"message"`

	Conversation struct {
		ID        int `json:"id"`
		ContactID int `json:"contact_id"`
		Meta      struct {
			Sender struct {
				PhoneNumber string `json:"phone_number"`
			} `json:"sender"`
		} `json:"meta"`
		Contact struct {
			Identifier string `json:"identifier"`
			Name       string `json:"name"`
		} `json:"contact"`
		ContactInbox struct {
			SourceID string `json:"source_id"`
		} `json:"contact_inbox"`
	} `json:"conversation"`
}

type Attachment struct {
	DataURL  string `json:"data_url"`
	FileType string `json:"file_type"`
}
