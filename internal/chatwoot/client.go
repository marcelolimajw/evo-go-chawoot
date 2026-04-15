package chatwoot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	BaseURL    string
	Token      string
	AccountID  string
	InboxID    string
	HTTPClient *http.Client
}

type ContactLookup struct {
	ID          int
	Name        string
	Identifier  string
	PhoneNumber string
	AvatarURL   string
}

func NewClient(baseURL, token, accountID, inboxID string) *Client {
	return &Client{
		BaseURL:    baseURL,
		Token:      token,
		AccountID:  accountID,
		InboxID:    inboxID,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) doRequest(method, endpoint string, payload interface{}) ([]byte, error) {
	urlStr := fmt.Sprintf("%s/api/v1/accounts/%s%s", c.BaseURL, c.AccountID, endpoint)
	var body io.Reader
	if payload != nil {
		jsonBody, _ := json.Marshal(payload)
		body = bytes.NewBuffer(jsonBody)
	}

	req, err := http.NewRequest(method, urlStr, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api_access_token", c.Token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("error %d: %s", resp.StatusCode, string(respData))
	}
	return respData, nil
}

func (c *Client) SearchContact(identifier string) (int, error) {
	contact, err := c.SearchContactDetails(identifier)
	if err != nil {
		return 0, err
	}
	if contact == nil {
		return 0, nil
	}
	return contact.ID, nil
}

func (c *Client) SearchContactDetails(identifier string) (*ContactLookup, error) {
	// 1. Busca direta pelo identificador completo (JID ou LID)
	contact, _ := c.searchContactDirect(identifier)
	if contact != nil {
		return contact, nil
	}

	// 2. Fallback: Se contiver números, tenta buscar apenas pelos números limpos
	// Isso ajuda a encontrar contatos que estão no Chatwoot com nomes manuais ou sem identifier
	cleanID := identifier
	if strings.Contains(identifier, "@") {
		cleanID = strings.Split(identifier, "@")[0]
	}

	// Remove qualquer caractere não numérico se for um JID de WhatsApp
	if !strings.Contains(identifier, "-") { // Não faz isso para grupos
		contact, _ = c.searchContactDirect(cleanID)
		if contact != nil {
			return contact, nil
		}
	}

	return nil, nil
}

func (c *Client) searchContactDirect(query string) (*ContactLookup, error) {
	endpoint := fmt.Sprintf("/contacts/search?q=%s", url.QueryEscape(query))
	data, err := c.doRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	var res struct {
		Payload []struct {
			ID          int     `json:"id"`
			Name        *string `json:"name"`
			Identifier  *string `json:"identifier"`
			PhoneNumber *string `json:"phone_number"`
			Thumbnail   *string `json:"thumbnail"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if len(res.Payload) > 0 {
		name := ""
		identifier := ""
		phone := ""
		if res.Payload[0].Name != nil {
			name = *res.Payload[0].Name
		}
		if res.Payload[0].Identifier != nil {
			identifier = *res.Payload[0].Identifier
		}
		if res.Payload[0].PhoneNumber != nil {
			phone = *res.Payload[0].PhoneNumber
		}
		avatar := ""
		if res.Payload[0].Thumbnail != nil {
			avatar = *res.Payload[0].Thumbnail
			if avatar != "" {
				fmt.Printf("[Chatwoot] Contact %s found. Current Avatar in Chatwoot: %s\n", identifier, avatar)
			}
		}
		return &ContactLookup{
			ID:          res.Payload[0].ID,
			Name:        name,
			Identifier:  identifier,
			PhoneNumber: phone,
			AvatarURL:   avatar,
		}, nil
	}
	return nil, nil
}

func (c *Client) CreateContact(name, identifier, phoneNumber, avatarURL string) (int, error) {
	inboxIDInt, _ := strconv.Atoi(c.InboxID)
	payload := ContactPayload{Name: name, Identifier: identifier, InboxID: inboxIDInt, PhoneNumber: phoneNumber}
	data, err := c.doRequest("POST", "/contacts", payload)
	if err != nil {
		return 0, err
	}
	var res ContactResponse
	json.Unmarshal(data, &res)
	contactID := res.Payload.Contact.ID
	if avatarURL != "" && contactID != 0 {
		_ = c.UpdateContact(contactID, map[string]interface{}{"avatar_url": avatarURL})
	}
	return contactID, nil
}

func (c *Client) UpdateContact(contactID int, data map[string]interface{}) error {
	_, err := c.doRequest("PUT", fmt.Sprintf("/contacts/%d", contactID), data)
	if err == nil {
		return nil
	}
	_, patchErr := c.doRequest("PATCH", fmt.Sprintf("/contacts/%d", contactID), data)
	return patchErr
}

func (c *Client) GetConversations(contactID int) (int, error) {
	data, err := c.doRequest("GET", fmt.Sprintf("/contacts/%d/conversations", contactID), nil)
	if err != nil {
		return 0, err
	}
	var res struct {
		Payload []struct {
			ID      int `json:"id"`
			InboxID int `json:"inbox_id"`
		} `json:"payload"`
	}
	json.Unmarshal(data, &res)
	for _, conv := range res.Payload {
		if fmt.Sprintf("%d", conv.InboxID) == c.InboxID {
			return conv.ID, nil
		}
	}
	return 0, nil
}

func (c *Client) CreateConversation(contactID, inboxID int, sourceID string) (int, error) {
	payload := ConversationPayload{SourceID: sourceID, InboxID: inboxID, ContactID: contactID}
	data, err := c.doRequest("POST", "/conversations", payload)
	if err != nil {
		return 0, err
	}
	var res ConversationResponse
	json.Unmarshal(data, &res)
	return res.ID, nil
}

func (c *Client) SendMessage(conversationID int, message, msgType, externalID string, private bool, contentAttr map[string]interface{}) (int, error) {
	payload := MessagePayload{
		Content:           message,
		MessageType:       msgType,
		Private:           private,
		ExternalID:        externalID,
		SourceID:          externalID,
		ContentAttributes: contentAttr,
	}
	data, err := c.doRequest("POST", fmt.Sprintf("/conversations/%d/messages", conversationID), payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		ID int `json:"id"`
	}
	_ = json.Unmarshal(data, &res)
	return res.ID, nil
}

func (c *Client) SendMessageWithAttachment(conversationID int, message, msgType, externalID string, fileBytes []byte, fileName, mimeType string, contentAttr map[string]interface{}) (int, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("message_type", msgType)
	_ = writer.WriteField("private", "false")

	// Adiciona conteúdo
	if message != "" {
		_ = writer.WriteField("content", message)
	}
	if externalID != "" {
		_ = writer.WriteField("external_id", externalID)
		_ = writer.WriteField("source_id", externalID)
	}
	if len(contentAttr) > 0 {
		if caJson, err := json.Marshal(contentAttr); err == nil {
			_ = writer.WriteField("content_attributes", string(caJson))
		}
	}

	// Cria o part do arquivo com Content-Type correto (importante para PDFs, áudios, etc.)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="attachments[]"; filename="%s"`, fileName))
	h.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(h)
	if err != nil {
		return 0, err
	}
	if _, err = part.Write(fileBytes); err != nil {
		return 0, err
	}
	writer.Close()

	urlStr := fmt.Sprintf("%s/api/v1/accounts/%s/conversations/%d/messages", c.BaseURL, c.AccountID, conversationID)
	req, _ := http.NewRequest("POST", urlStr, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Add("api_access_token", c.Token)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respData, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("status %d: %s", resp.StatusCode, string(respData))
	}
	var res struct {
		ID int `json:"id"`
	}
	_ = json.Unmarshal(respData, &res)
	return res.ID, nil
}

func (c *Client) DeleteMessage(conversationID, messageID int) error {
	_, err := c.doRequest("DELETE", fmt.Sprintf("/conversations/%d/messages/%d", conversationID, messageID), nil)
	return err
}
func (c *Client) UpdateMessage(conversationID, messageID int, externalID string) error {
	payload := map[string]interface{}{
		"message": map[string]interface{}{
			"external_id": externalID,
			"source_id":   externalID,
		},
	}
	resp, err := c.doRequest("PATCH", fmt.Sprintf("/conversations/%d/messages/%d", conversationID, messageID), payload)
	if err != nil {
		return err
	}
	_ = resp
	return nil
}
