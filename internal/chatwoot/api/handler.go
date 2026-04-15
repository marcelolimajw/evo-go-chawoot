package chatwoot_api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EvolutionAPI/evolution-go/internal/chatwoot"
	"github.com/EvolutionAPI/evolution-go/pkg/config"
	instance_model "github.com/EvolutionAPI/evolution-go/pkg/instance/model"
	instance_service "github.com/EvolutionAPI/evolution-go/pkg/instance/service"
	message_service "github.com/EvolutionAPI/evolution-go/pkg/message/service"
	send_service "github.com/EvolutionAPI/evolution-go/pkg/sendMessage/service"
	"github.com/gin-gonic/gin"
)

type WebhookHandler struct {
	Config          *config.Config
	SendService     send_service.SendService
	InstanceService instance_service.InstanceService
	MessageService  message_service.MessageService
}

var (
	processedCWMsgMutex sync.Mutex
	processedCWMsgIDs   = make(map[string]time.Time)
)

func markOrCheckDuplicateCWMessage(key string, ttl time.Duration) bool {
	now := time.Now()
	processedCWMsgMutex.Lock()
	defer processedCWMsgMutex.Unlock()

	if ts, exists := processedCWMsgIDs[key]; exists && now.Sub(ts) < ttl {
		return true
	}

	processedCWMsgIDs[key] = now

	if len(processedCWMsgIDs) > 5000 {
		for k, ts := range processedCWMsgIDs {
			if now.Sub(ts) > 10*time.Minute {
				delete(processedCWMsgIDs, k)
			}
		}
	}

	return false
}

func NewWebhookHandler(cfg *config.Config, sendService send_service.SendService, instService instance_service.InstanceService, msgService message_service.MessageService) *WebhookHandler {
	return &WebhookHandler{
		Config:          cfg,
		SendService:     sendService,
		InstanceService: instService,
		MessageService:  msgService,
	}
}

// -- ENDPOINTS DE GERENCIAMENTO (PARA MULTI-CONTA) --

func (h *WebhookHandler) ListInstances(c *gin.Context) {
	if c.GetHeader("apikey") != h.Config.GlobalApiKey {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	c.JSON(http.StatusOK, chatwoot.GetAllConfigs())
}

func (h *WebhookHandler) SetInstance(c *gin.Context) {
	if c.GetHeader("apikey") != h.Config.GlobalApiKey {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var cfg chatwoot.InstanceConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	name := c.Query("instance")
	if name == "" {
		name = cfg.Name
	}
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "instance name required in query or body"})
		return
	}

	if err := chatwoot.SetConfig(name, cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "saved", "instance": name})
}

func (h *WebhookHandler) DeleteInstance(c *gin.Context) {
	if c.GetHeader("apikey") != h.Config.GlobalApiKey {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	name := c.Param("name")
	if err := chatwoot.DeleteConfig(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted", "instance": name})
}

// -- WEBHOOK PRINCIPAL --

func (h *WebhookHandler) HandleWebhook(c *gin.Context) {
	var payload chatwoot.WebhookPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	instanceName := c.Param("instanceName")
	if instanceName == "" {
		instanceName = c.DefaultQuery("instance", "default")
	}

	event := payload.Event
	msgType := payload.Message.MessageType
	if msgType == "" {
		msgType = payload.MessageType
	}
	content := payload.Message.Content
	if content == "" {
		content = payload.Content
	}
	contentLower := strings.ToLower(strings.TrimSpace(content))
	isDeletedNotice := strings.Contains(contentLower, "mensagem foi exclu") ||
		strings.Contains(contentLower, "mensagem apagada") ||
		strings.Contains(contentLower, "message was deleted")

	msgID := payload.Message.ID
	if msgID == 0 {
		msgID = payload.ID
	}

	externalID := payload.Message.ExternalID
	if externalID == "" {
		externalID = payload.ExternalID
	}
	if externalID == "" {
		externalID = payload.SourceID
	}
	if externalID == "" {
		externalID = payload.Message.SourceID
	}

	isDeleted := payload.Message.ContentAttributes.Deleted || payload.ContentAttributes.Deleted || isDeletedNotice
	replyToCWMsgID := payload.Message.ContentAttributes.InReplyTo
	if replyToCWMsgID == 0 {
		replyToCWMsgID = payload.ContentAttributes.InReplyTo
	}

	convID := payload.Conversation.ID
	if convID == 0 {
		convID = payload.Message.ConversationID
	}

	cwCfg, _ := chatwoot.GetConfig(instanceName)
	payloadInboxID := payload.Message.InboxID
	if payloadInboxID == 0 {
		payloadInboxID = payload.InboxID
	}
	if cwCfg.InboxID != "" && payloadInboxID != 0 {
		cfgInboxID, convErr := strconv.Atoi(cwCfg.InboxID)
		if convErr == nil && cfgInboxID != payloadInboxID {
			c.JSON(http.StatusOK, gin.H{"status": "ignored_inbox_mismatch"})
			return
		}
	}

	if event == "message_created" && msgType == "outgoing" && msgID != 0 {
		dupKey := fmt.Sprintf("cwmsg:%d", msgID)
		if markOrCheckDuplicateCWMessage(dupKey, 2*time.Minute) {
			c.JSON(http.StatusOK, gin.H{"status": "ignored_duplicate_message_created"})
			return
		}
	}

	if event == "message_updated" && isDeleted {
		waID := ""
		if strings.HasPrefix(externalID, "WAID:") {
			waID = strings.TrimPrefix(externalID, "WAID:")
		} else {
			// Tenta mapping local
			if localID, ok := chatwoot.GetMapping(msgID); ok {
				waID = localID
			}
		}

		if waID != "" {

			// Obter JID do chat
			phone := payload.Conversation.Meta.Sender.PhoneNumber
			if phone == "" {
				phone = payload.Conversation.Contact.Identifier
			}
			if phone == "" {
				phone = payload.Conversation.ContactInbox.SourceID
			}

			phone = strings.TrimPrefix(phone, "+")
			fullJid := phone
			if !strings.Contains(phone, "@") {
				isGroup := strings.Contains(phone, "-") || len(phone) > 15
				if strings.HasPrefix(phone, "147") && len(phone) >= 15 {
					fullJid = phone + "@lid"
				} else if isGroup {
					fullJid = phone + "@g.us"
				} else {
					fullJid = phone + "@s.whatsapp.net"
				}
			}

			allInstances, _ := h.InstanceService.GetAll()
			var instance *instance_model.Instance
			for _, inst := range allInstances {
				if inst.Name == instanceName || inst.Id == instanceName {
					instance = inst
					break
				}
			}

			if instance != nil {
				delData := &message_service.MessageStruct{
					Chat:      fullJid,
					MessageID: waID,
				}
				h.MessageService.DeleteMessageEveryone(delData, instance)
				c.JSON(http.StatusOK, gin.H{"status": "deleted_on_whatsapp"})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "ignored_update"})
		return
	}

	// Alguns cenários do Chatwoot geram message_created/outgoing com texto de exclusão.
	// Isso não deve ser reenviado para o WhatsApp para evitar mensagem duplicada.
	if event == "message_created" && msgType == "outgoing" && isDeletedNotice {
		c.JSON(http.StatusOK, gin.H{"status": "ignored_deleted_notice"})
		return
	}

	if event != "message_created" || msgType != "outgoing" {
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
		return
	}

	if strings.HasPrefix(externalID, "WAID:") {
		c.JSON(http.StatusOK, gin.H{"status": "ignored_echo_via_externalid"})
		return
	}

	// Filtro de Eco
	echoKey := fmt.Sprintf("%d:%s", convID, content)
	chatwoot.EchoMutex.Lock()
	lastTime, found := chatwoot.EchoCache[echoKey]
	if found && time.Since(lastTime) < 10*time.Second {
		delete(chatwoot.EchoCache, echoKey)
		chatwoot.EchoMutex.Unlock()
		c.JSON(http.StatusOK, gin.H{"status": "echo_suppressed"})
		return
	}
	chatwoot.EchoMutex.Unlock()

	allInstances, _ := h.InstanceService.GetAll()
	var instance *instance_model.Instance
	for _, inst := range allInstances {
		if inst.Name == instanceName || inst.Id == instanceName {
			instance = inst
			break
		}
	}

	if instance == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	// Lógica de Assinatura
	senderName := payload.Message.Sender.Name
	if senderName == "" {
		senderName = payload.Sender.Name
	}

	if cwCfg.EnableSignature && senderName != "" {
		content = fmt.Sprintf("*%s:* \n%s", senderName, content)
	}

	phone := payload.Conversation.Meta.Sender.PhoneNumber
	if phone == "" {
		phone = payload.Conversation.Contact.Identifier
	}
	if phone == "" {
		phone = payload.Conversation.ContactInbox.SourceID
	}
	if phone == "" {
		phone = payload.Message.Sender.Identifier
	}

	if phone == "" {
		c.JSON(http.StatusOK, gin.H{"status": "no identifier found"})
		return
	}

	phone = strings.TrimPrefix(phone, "+")
	fullJid := phone
	if !strings.Contains(phone, "@") {
		isGroup := strings.Contains(phone, "-") || len(phone) > 15
		if strings.HasPrefix(phone, "147") && len(phone) >= 15 {
			fullJid = phone + "@lid"
		} else if isGroup {
			fullJid = phone + "@g.us"
		} else {
			fullJid = phone + "@s.whatsapp.net"
		}
	}

	attachments := payload.Message.Attachments
	if len(attachments) == 0 {
		attachments = payload.Attachments
	}
	if len(attachments) > 0 {
		mediaURL := strings.ToLower(strings.TrimSpace(attachments[0].DataURL))
		mediaKey := "mediaurl:" + mediaURL
		if markOrCheckDuplicateCWMessage(mediaKey, 30*time.Second) {
			c.JSON(http.StatusOK, gin.H{"status": "ignored_duplicate_media"})
			return
		}
	}
	if len(attachments) == 0 && strings.TrimSpace(content) == "" {
		c.JSON(http.StatusOK, gin.H{"status": "ignored_empty"})
		return
	}

	quoted := send_service.QuotedStruct{}
	if replyToCWMsgID != 0 {
		if waQuotedID, participant, _, ok := chatwoot.GetQuoteByCW(replyToCWMsgID); ok {
			quoted.MessageID = waQuotedID
			quoted.Participant = participant
		}
	}

	var msgSend *send_service.MessageSendStruct
	var err error
	if len(attachments) > 0 {
		att := attachments[0]
		mType := "document"
		fName := "file"
		lowerType := strings.ToLower(att.FileType)
		lowerURL := strings.ToLower(att.DataURL)

		if strings.Contains(lowerType, "audio") || strings.HasSuffix(lowerURL, ".ogg") || strings.HasSuffix(lowerURL, ".mp3") || strings.HasSuffix(lowerURL, ".oga") {
			mType = "audio"
			fName = "audio.ogg"
		} else if strings.Contains(lowerType, "image") || strings.HasSuffix(lowerURL, ".jpg") || strings.HasSuffix(lowerURL, ".jpeg") || strings.HasSuffix(lowerURL, ".png") {
			mType = "image"
			fName = "image.jpg"
		} else if strings.Contains(lowerType, "video") || strings.HasSuffix(lowerURL, ".mp4") {
			mType = "video"
			fName = "video.mp4"
		}

		mediaData := &send_service.MediaStruct{
			Number:   fullJid,
			Caption:  content,
			Url:      att.DataURL,
			Filename: fName,
			Type:     mType,
			Quoted:   quoted,
		}
		msgSend, err = h.SendService.SendMediaUrl(mediaData, instance)
	} else if content != "" {
		formatJid := false
		textData := &send_service.TextStruct{
			Number:    fullJid,
			Text:      content,
			FormatJid: &formatJid,
			Quoted:    quoted,
		}
		msgSend, err = h.SendService.SendText(textData, instance)
	}

	if err == nil && msgSend != nil && msgID != 0 {
		// Salva no mapeamento local para garantir deleção (Chatwoot API immutability fallback)
		chatwoot.SetMapping(msgID, msgSend.Info.ID)

		// Tenta atualizar no Chatwoot (opcional, mas bom pra ter no painel)
		cwClient := chatwoot.NewClient(cwCfg.URL, cwCfg.Token, cwCfg.AccountID, cwCfg.InboxID)
		upErr := cwClient.UpdateMessage(payload.Conversation.ID, msgID, "WAID:"+msgSend.Info.ID)
		_ = upErr
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "sent"})
}

func (h *WebhookHandler) HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "up", "service": "chatwoot-multi-instance-signature-ready"})
}
