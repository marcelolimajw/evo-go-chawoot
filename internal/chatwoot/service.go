package chatwoot

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EvolutionAPI/evolution-go/pkg/config"
	storage_interfaces "github.com/EvolutionAPI/evolution-go/pkg/storage/interfaces"
	minio_storage "github.com/EvolutionAPI/evolution-go/pkg/storage/minio"
	"github.com/EvolutionAPI/evolution-go/pkg/utils"
	"github.com/chai2010/webp"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Global Echo Cache to prevent loops
var (
	EchoCache = make(map[string]time.Time)
	EchoMutex sync.Mutex
)

func AddToEchoCache(convID int, content string) {
	key := fmt.Sprintf("%d:%s", convID, content)
	EchoMutex.Lock()
	EchoCache[key] = time.Now()
	EchoMutex.Unlock()
}

type Service struct {
	Config       *config.Config
	MediaStorage storage_interfaces.MediaStorage
}

func NewService(cfg *config.Config) *Service {
	if err := LoadConfigs(); err != nil {
		fmt.Printf("[Chatwoot] failed to load configs: %v\n", err)
	}

	var storage storage_interfaces.MediaStorage
	if cfg.MinioEnabled {
		var err error
		storage, err = minio_storage.NewMinioMediaStorage(
			cfg.MinioEndpoint,
			cfg.MinioAccessKey,
			cfg.MinioSecretKey,
			cfg.MinioBucket,
			cfg.MinioRegion,
			cfg.MinioUseSSL,
		)
		if err != nil {
			fmt.Printf("[Chatwoot] failed to initialize minio storage: %v\n", err)
		}
	}

	return &Service{
		Config:       cfg,
		MediaStorage: storage,
	}
}

func (s *Service) GetClientForInstance(instanceName string) *Client {
	if cfg, ok := GetConfig(instanceName); ok {
		return NewClient(cfg.URL, cfg.Token, cfg.AccountID, cfg.InboxID)
	}

	if instanceName != "" {
		fmt.Printf("[Chatwoot] config not found for instance '%s'\n", instanceName)
	}

	if s.Config.ChatwootUrl != "" && s.Config.ChatwootToken != "" && s.Config.ChatwootAccountId != "" && s.Config.ChatwootInboxId != "" {
		return NewClient(s.Config.ChatwootUrl, s.Config.ChatwootToken, s.Config.ChatwootAccountId, s.Config.ChatwootInboxId)
	}

	return nil
}

const mirrorDeviceTitle = "📲 Enviado pelo aparelho:\n"

func (s *Service) HandleWhatsAppMessage(evt *events.Message, instance string, waClient *whatsmeow.Client) error {
	client := s.GetClientForInstance(instance)
	if client == nil {
		return fmt.Errorf("chatwoot client not available for instance '%s'", instance)
	}

	isGroup := evt.Info.IsGroup
	targetJID := evt.Info.Chat

	if !isGroup {
		// RADICAL LID SWAP: Se for LID, tenta pegar o Sender ou converter
		if targetJID.Server == "lid" {
			if !evt.Info.IsFromMe && evt.Info.Sender.Server == "s.whatsapp.net" {
				targetJID = evt.Info.Sender.ToNonAD()
			}
		}
	}

	// Tenta resolver LID para JID de WhatsApp real (PN) de várias formas
	if targetJID.Server == "lid" {
		// 1. Pelo Store do whatsmeow
		if waClient != nil && waClient.Store != nil {
			altJID, _ := waClient.Store.GetAltJID(context.Background(), targetJID)
			if !altJID.IsEmpty() {
				targetJID = altJID
			}
		}

		// 2. Se for uma mensagem recebida e o Sender for um JID real, usamos o Sender como base se for conversa direta
		if targetJID.Server == "lid" && !isGroup && !evt.Info.IsFromMe {
			if evt.Info.Sender.Server == "s.whatsapp.net" {
				targetJID = evt.Info.Sender.ToNonAD()
			}
		}
	}
	targetID := targetJID.String()
	// source_id do contact_inbox: apenas os dígitos em conversa direta, para o
	// disparo em massa reencontrar a conversa; JID completo em grupo/LID.
	sourceID := chatwootSourceID(targetID, isGroup)

	name := ""
	if !evt.Info.IsFromMe {
		name = evt.Info.PushName
	}

	if isGroup {
		if name == "" || !strings.HasSuffix(name, "(GROUP)") {
			if waClient != nil {
				groupInfo, err := waClient.GetGroupInfo(context.Background(), evt.Info.Chat)
				if err == nil && groupInfo != nil && groupInfo.Name != "" {
					name = groupInfo.Name + " (GROUP)"
				}
			}
		}
		if name == "" {
			name = targetID + " (GROUP)"
		}
	} else if name == "" {
		name = jidUserPart(targetID)
		if name == "" {
			name = targetID
		}
	}
	phoneNumber := ""
	if !isGroup {
		phoneNumber = phoneNumberFromJID(targetID)
	}
	// Tenta encontrar o contato no Chatwoot
	contactDetails, _ := client.SearchContactDetails(targetID)
	contactID := 0
	if contactDetails != nil {
		contactID = contactDetails.ID
	}

	// Lógica especial para o 9º dígito no Brasil se não encontrar pelo ID padrão
	if contactID == 0 && strings.HasPrefix(targetID, "55") && !isGroup {
		// Se tem 13 dígitos (sem o 9), tenta com 11 dígitos no user part (com o 9)
		// Ou vice-versa. Ex: 55 11 9XXXX XXXX (13 chars) vs 55 11 XXXX XXXX (12 chars)
		// Na verdade, 55 + DDD (2) + 9 + 8 digits = 13.
		userPart := strings.Split(targetID, "@")[0]
		if len(userPart) == 13 { // Provavelmente com o 9
			altUser := userPart[:4] + userPart[5:] // remove o 9
			contactDetails, _ = client.SearchContactDetails(altUser + "@s.whatsapp.net")
		} else if len(userPart) == 12 { // Provavelmente sem o 9
			altUser := userPart[:4] + "9" + userPart[4:] // adiciona o 9
			contactDetails, _ = client.SearchContactDetails(altUser + "@s.whatsapp.net")
		}
		if contactDetails != nil {
			contactID = contactDetails.ID
		}
	}

	avatarURL := ""
	// Melhoria: Só consideramos que o contato TEM foto se o link começar com "http"
	// Isso evita que a API ignore o contato quando o Chatwoot retorna um avatar padrão do sistema (/assets/...)
	hasRealAvatar := contactDetails != nil && strings.HasPrefix(contactDetails.AvatarURL, "http")

	if contactID == 0 || !hasRealAvatar {
		avatarURL = s.getAvatarURL(waClient, targetID)
	}

	if contactID == 0 {
		contactID, _ = client.CreateContact(name, targetID, phoneNumber, avatarURL, sourceID)
	} else {
		updateData := map[string]interface{}{}

		if isGroup {
			if name != "" {
				shouldUpdateName := true
				if contactDetails != nil {
					shouldUpdateName = strings.TrimSpace(strings.ToLower(contactDetails.Name)) != strings.TrimSpace(strings.ToLower(name))
				}
				if shouldUpdateName {
					updateData["name"] = name
				}
			}
			if avatarURL != "" {
				updateData["avatar_url"] = avatarURL
			}
		} else if !evt.Info.IsFromMe && evt.Info.PushName != "" && contactDetails != nil {
			if shouldUpdateContactName(contactDetails, targetID) {
				updateData["name"] = evt.Info.PushName
			}
			if avatarURL != "" {
				updateData["avatar_url"] = avatarURL
			}
		}

		if len(updateData) > 0 {
			_ = client.UpdateContact(contactID, updateData)
		}
	}

	convID, _ := client.GetConversations(contactID)
	if convID == 0 {
		inboxID, _ := strconv.Atoi(client.InboxID)
		convID, _ = client.CreateConversation(contactID, inboxID, sourceID)
	}

	msgType := "incoming"
	if evt.Info.IsFromMe {
		msgType = "outgoing"
	}

	// Desembrulha a mensagem uma única vez: o mesmo msg é usado para o texto, a
	// mídia, a resposta citada e a revogação. Uma mensagem é "encaminhada" quando
	// veio no wrapper botForwardedMessage ou quando o WhatsApp marcou
	// ContextInfo.isForwarded — é este último o sinal usado em encaminhadas comuns
	// (localização, foto, texto), que não usam nenhum wrapper.
	msg, isForwarded := unwrapMessageContent(evt.Message)
	if msg == nil {
		return nil
	}
	if isForwardedContext(msg) {
		isForwarded = true
	}
	logForwardDiagnostics(evt, msg, isForwarded)

	content := extractMessageContentFrom(msg, waClient)
	isEdit := evt.IsEdit || evt.Info.Edit == "1"

	// Aplica as decorações de texto. Os ramos de mídia remontam o content do
	// zero mais abaixo, então applyDecorations é chamado de novo antes do envio.
	content = applyDecorations(content, isEdit, isForwarded)
	if header := senderHeader(evt, isGroup); header != "" {
		content = header + content
	}

	isFromMe := evt.Info.IsFromMe
	if isFromMe {
		content = mirrorDeviceTitle + content
		AddToEchoCache(convID, content)
	}

	deletedTargetMsgID := ""
	if msg.ProtocolMessage != nil && msg.ProtocolMessage.GetType() == waE2E.ProtocolMessage_REVOKE {
		if key := msg.ProtocolMessage.GetKey(); key != nil && key.GetID() != "" {
			deletedTargetMsgID = key.GetID()
		}
	}
	if deletedTargetMsgID != "" {
		if cwMsgID, ok := GetCWByWA(deletedTargetMsgID); ok {
			if delErr := client.DeleteMessage(convID, cwMsgID); delErr == nil {
				DeleteByWA(deletedTargetMsgID)
				return nil
			}
		}
		// Avoid creating a new "message deleted" bubble in Chatwoot when mapping is missing.
		return nil
	}

	var err error
	cwMsgID := 0
	isMedia := false
	var fileBytes []byte
	var fileName, mimeType string

	if waClient != nil {
		if img := msg.GetImageMessage(); img != nil {
			isMedia = true
			fileBytes, err = waClient.Download(context.Background(), img)
			mt := img.GetMimetype()
			if mt == "" {
				mt = "image/jpeg"
			}
			fileName = "image" + mimetypeToExt(mt)
			mimeType = mt
			// Legenda da imagem - preserva o cabeçalho de grupo quando aplicável
			if cap := img.GetCaption(); cap != "" {
				if isGroup && !evt.Info.IsFromMe {
					// Reconstrói o cabeçalho do remetente do grupo como feito acima
					senderName := evt.Info.PushName
					if senderName == "" {
						senderName = evt.Info.Sender.User
					}
					senderPhone := evt.Info.Sender.User
					if strings.HasPrefix(senderPhone, "55") && len(senderPhone) >= 12 {
						content = fmt.Sprintf("**%s - %s:**\n\n%s", senderPhone, senderName, cap)
					} else {
						content = fmt.Sprintf("**%s:**\n\n%s", senderName, cap)
					}
				} else {
					content = cap
				}
			} else {
				// Sem legenda: mantém o cabeçalho de grupo (se for grupo) em vez de apagar
				if isGroup && !evt.Info.IsFromMe {
					senderName := evt.Info.PushName
					if senderName == "" {
						senderName = evt.Info.Sender.User
					}
					senderPhone := evt.Info.Sender.User
					if strings.HasPrefix(senderPhone, "55") && len(senderPhone) >= 12 {
						content = fmt.Sprintf("**%s - %s:**\n\n", senderPhone, senderName)
					} else {
						content = fmt.Sprintf("**%s:**\n\n", senderName)
					}
				} else {
					content = ""
				}
			}
		} else if vid := msg.GetVideoMessage(); vid != nil {
			isMedia = true
			fileBytes, err = waClient.Download(context.Background(), vid)
			mt := vid.GetMimetype()
			if mt == "" {
				mt = "video/mp4"
			}
			fileName = "video" + mimetypeToExt(mt)
			mimeType = mt
			// Legenda do vídeo - preserva o cabeçalho de grupo quando aplicável
			if cap := vid.GetCaption(); cap != "" {
				if isGroup && !evt.Info.IsFromMe {
					senderName := evt.Info.PushName
					if senderName == "" {
						senderName = evt.Info.Sender.User
					}
					senderPhone := evt.Info.Sender.User
					if strings.HasPrefix(senderPhone, "55") && len(senderPhone) >= 12 {
						content = fmt.Sprintf("**%s - %s:**\n\n%s", senderPhone, senderName, cap)
					} else {
						content = fmt.Sprintf("**%s:**\n\n%s", senderName, cap)
					}
				} else {
					content = cap
				}
			} else {
				if isGroup && !evt.Info.IsFromMe {
					senderName := evt.Info.PushName
					if senderName == "" {
						senderName = evt.Info.Sender.User
					}
					senderPhone := evt.Info.Sender.User
					if strings.HasPrefix(senderPhone, "55") && len(senderPhone) >= 12 {
						content = fmt.Sprintf("**%s - %s:**\n\n", senderPhone, senderName)
					} else {
						content = fmt.Sprintf("**%s:**\n\n", senderName)
					}
				} else {
					content = ""
				}
			}
		} else if aud := msg.GetAudioMessage(); aud != nil {
			isMedia = true
			fileBytes, err = waClient.Download(context.Background(), aud)
			mt := aud.GetMimetype()
			if mt == "" {
				mt = "audio/ogg; codecs=opus"
			}
			// remove parameters like "; codecs=opus"
			if strings.Contains(mt, ";") {
				mt = strings.Split(mt, ";")[0]
			}
			fileName = "audio.ogg"
			if strings.Contains(mt, "mp4") || strings.Contains(mt, "mpeg") || strings.Contains(mt, "mp3") {
				fileName = "audio.mp3"
			}
			mimeType = mt
			if isGroup && !evt.Info.IsFromMe {
				senderName := evt.Info.PushName
				if senderName == "" {
					senderName = evt.Info.Sender.User
				}
				senderPhone := evt.Info.Sender.User
				if strings.HasPrefix(senderPhone, "55") && len(senderPhone) >= 12 {
					content = fmt.Sprintf("**%s - %s:**\n\n", senderPhone, senderName)
				} else {
					content = fmt.Sprintf("**%s:**\n\n", senderName)
				}
			} else {
				content = ""
			}
		} else if doc := msg.GetDocumentMessage(); doc != nil {
			isMedia = true
			fileBytes, err = waClient.Download(context.Background(), doc)
			fileName = doc.GetFileName()
			if fileName == "" {
				fileName = doc.GetTitle()
			}
			if fileName == "" {
				fileName = "document"
			}
			mimeType = doc.GetMimetype()
			// Legenda do documento - preserva o cabeçalho de grupo quando aplicável
			if cap := doc.GetCaption(); cap != "" {
				if isGroup && !evt.Info.IsFromMe {
					senderName := evt.Info.PushName
					if senderName == "" {
						senderName = evt.Info.Sender.User
					}
					senderPhone := evt.Info.Sender.User
					if strings.HasPrefix(senderPhone, "55") && len(senderPhone) >= 12 {
						content = fmt.Sprintf("**%s - %s:**\n\n%s", senderPhone, senderName, cap)
					} else {
						content = fmt.Sprintf("**%s:**\n\n%s", senderName, cap)
					}
				} else {
					content = cap
				}
			} else {
				if isGroup && !evt.Info.IsFromMe {
					senderName := evt.Info.PushName
					if senderName == "" {
						senderName = evt.Info.Sender.User
					}
					senderPhone := evt.Info.Sender.User
					if strings.HasPrefix(senderPhone, "55") && len(senderPhone) >= 12 {
						content = fmt.Sprintf("**%s - %s:**\n\n", senderPhone, senderName)
					} else {
						content = fmt.Sprintf("**%s:**\n\n", senderName)
					}
				} else {
					content = ""
				}
			}
		} else if stk := msg.GetStickerMessage(); stk != nil {
			isMedia = true
			fileBytes, err = waClient.Download(context.Background(), stk)
			// Convert webp sticker to png so Chatwoot / browsers render consistently
			if err == nil && len(fileBytes) > 0 {
				// Try decode webp and encode to png
				webpReader := bytes.NewReader(fileBytes)
				if img, decErr := webp.Decode(webpReader); decErr == nil {
					var pngBuf bytes.Buffer
					if encErr := png.Encode(&pngBuf, img); encErr == nil {
						fileBytes = pngBuf.Bytes()
						fileName = "sticker.png"
						mimeType = "image/png"
					} else {
						// Fallback to send original webp if png encode fails
						fileName = "sticker.webp"
						mimeType = "image/webp"
					}
				} else {
					fileName = "sticker.webp"
					mimeType = "image/webp"
				}
			} else {
				fileName = "sticker.webp"
				mimeType = "image/webp"
			}
			content = ""
			if isGroup && !evt.Info.IsFromMe {
				senderName := evt.Info.PushName
				if senderName == "" {
					senderName = evt.Info.Sender.User
				}
				senderPhone := evt.Info.Sender.User
				if strings.HasPrefix(senderPhone, "55") && len(senderPhone) >= 12 {
					content = fmt.Sprintf("**%s - %s:**\n\n[Figurinha]", senderPhone, senderName)
				} else {
					content = fmt.Sprintf("**%s:**\n\n[Figurinha]", senderName)
				}
			} else {
				content = "[Figurinha]"
			}
		} else if loc := msg.GetLocationMessage(); loc != nil {
			// Mensagem de Localização → Link do Google Maps
			content = senderHeader(evt, isGroup) + renderLocation(loc)
		}
	}

	// Extração de mensagem citada (StanzaID)
	var stanzaID string
	if msg != nil {
		if msg.GetExtendedTextMessage() != nil {
			stanzaID = msg.GetExtendedTextMessage().GetContextInfo().GetStanzaID()
		} else if msg.GetImageMessage() != nil {
			stanzaID = msg.GetImageMessage().GetContextInfo().GetStanzaID()
		} else if msg.GetAudioMessage() != nil {
			stanzaID = msg.GetAudioMessage().GetContextInfo().GetStanzaID()
		} else if msg.GetDocumentMessage() != nil {
			stanzaID = msg.GetDocumentMessage().GetContextInfo().GetStanzaID()
		} else if msg.GetVideoMessage() != nil {
			stanzaID = msg.GetVideoMessage().GetContextInfo().GetStanzaID()
		} else if msg.GetStickerMessage() != nil {
			stanzaID = msg.GetStickerMessage().GetContextInfo().GetStanzaID()
		} else if msg.GetReactionMessage() != nil {
			if key := msg.GetReactionMessage().GetKey(); key != nil {
				stanzaID = key.GetID()
			}
		} else if msg.GetEncReactionMessage() != nil {
			// EncReactionMessage has a TargetMessageKey
			if key := msg.GetEncReactionMessage().TargetMessageKey; key != nil {
				stanzaID = key.GetID()
			}
		}
	}

	contentAttributes := make(map[string]interface{})
	if stanzaID != "" {
		contentAttributes["in_reply_to_external_id"] = stanzaID
		if cwQuotedID, ok := GetCWByWA(stanzaID); ok && cwQuotedID != 0 {
			contentAttributes["in_reply_to"] = cwQuotedID
		}
	}

	externalID := "WAID:" + evt.Info.ID
	if deletedTargetMsgID != "" {
		externalID = "WAID:" + deletedTargetMsgID
	}

	if isMedia && err == nil && fileBytes != nil {
		if s.MediaStorage != nil {
			// Tratamento especial para ÁUDIO e VÍDEO (Modo Link Direto + Custom Player via Dashboard Script)
			isAudio := strings.Contains(mimeType, "audio")
			isVideo := strings.Contains(mimeType, "video")

			if isAudio || isVideo {
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				ext := mimetypeToExt(mimeType)
				storageName := fmt.Sprintf("%s/%s_%d%s", instance, evt.Info.ID, time.Now().Unix(), ext)

				fmt.Printf("[Chatwoot] Uploading %s to Minio for direct playback\n", mimeType)
				s3URL, uploadErr := s.MediaStorage.Store(ctx, fileBytes, storageName, mimeType)
				if uploadErr == nil {
					label := "▶️ **Mensagem de voz**"
					if isVideo {
						label = "▶️ **Vídeo**"
					}

					if content != "" {
						content = fmt.Sprintf("%s\n\n%s [ ](%s)", content, label, s3URL)
					} else {
						content = fmt.Sprintf("%s [ ](%s)", label, s3URL)
					}
					isMedia = false // Enviará como texto para ativar o Custom Player do Chatwoot
				}
			} else {
				// Para IMAGENS e FIGURINHAS, mantemos nativo com backup em background
				go func(data []byte, name string, mt string) {
					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					defer cancel()
					ext := mimetypeToExt(mt)
					storageName := fmt.Sprintf("%s/%s_%d%s", instance, evt.Info.ID, time.Now().Unix(), ext)
					_, _ = s.MediaStorage.Store(ctx, data, storageName, mt)
				}(fileBytes, fileName, mimeType)
			}
		}

		// Envio para o Chatwoot
		if isMedia {
			content = applyDecorations(content, isEdit, isForwarded)
			cwMsgID, err = client.SendMessageWithAttachment(convID, content, msgType, externalID, fileBytes, fileName, mimeType, contentAttributes, isFromMe)
		} else {
			content = applyDecorations(content, isEdit, isForwarded)
			cwMsgID, err = client.SendMessage(convID, content, msgType, externalID, isFromMe, contentAttributes)
		}
	} else {
		// Mídia sem download (localização não tem arquivo) ou texto puro: os
		// ramos de mídia não passaram por aqui, mas a localização também monta o
		// content do zero e precisa receber "[Editado]"/"↪" de volta.
		content = applyDecorations(content, isEdit, isForwarded)
		cwMsgID, err = client.SendMessage(convID, content, msgType, externalID, isFromMe, contentAttributes)
	}

	if err == nil && cwMsgID != 0 {
		participant := ""
		if evt.Info.IsFromMe {
			if waClient != nil && waClient.Store != nil && waClient.Store.ID != nil {
				participant = waClient.Store.ID.ToNonAD().String()
			}
		} else {
			participant = evt.Info.Sender.ToNonAD().String()
		}
		SetMappingMeta(cwMsgID, evt.Info.ID, participant, evt.Info.Chat.String())
	}

	// Limpeza periódica do mapa de IDs (mantém apenas os últimos 5000)
	go cleanupMappingIfNeeded()

	return err
}

// cleanupMappingIfNeeded limpa entradas antigas do mapa quando passa de 5000
func cleanupMappingIfNeeded() {
	mapMutex.Lock()
	defer mapMutex.Unlock()
	if len(msgMappings) > 5000 {
		count := 0
		for k := range msgMappings {
			delete(msgMappings, k)
			count++
			if count >= 2500 {
				break
			} // Remove metade
		}
		SaveMappings()
	}
}

// mimetypeToExt converte mimetype para extensão de arquivo
func mimetypeToExt(mt string) string {
	switch {
	case strings.HasPrefix(mt, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(mt, "image/png"):
		return ".png"
	case strings.HasPrefix(mt, "image/gif"):
		return ".gif"
	case strings.HasPrefix(mt, "image/webp"):
		return ".webp"
	case strings.HasPrefix(mt, "video/mp4"):
		return ".mp4"
	case strings.HasPrefix(mt, "video/"):
		return ".mp4"
	case strings.HasPrefix(mt, "audio/ogg"):
		return ".ogg"
	case strings.HasPrefix(mt, "audio/mp"):
		return ".mp3"
	case strings.HasPrefix(mt, "audio/"):
		return ".ogg"
	default:
		return ""
	}
}

func jidUserPart(jid string) string {
	part := jid
	if strings.Contains(part, "@") {
		part = strings.Split(part, "@")[0]
	}
	if strings.Contains(part, ":") {
		part = strings.Split(part, ":")[0]
	}
	return strings.TrimSpace(part)
}

func phoneNumberFromJID(jid string) string {
	user := jidUserPart(jid)
	if user == "" {
		return ""
	}
	if strings.HasPrefix(user, "+") {
		return user
	}
	return "+" + user
}

// chatwootSourceID deriva o source_id gravado no contact_inbox.
//
// A campanha em massa do Chatwoot resolve a conversa pelo source_id e, em inbox
// Channel::Api, procura somente os dígitos do telefone. Por isso, em conversa
// direta devolvemos apenas o número: assim o disparo encontra o contact_inbox já
// existente e reabre a conversa em vez de criar outra.
//
// Grupo (@g.us) e LID (@lid) não possuem telefone, então precisam manter o JID
// completo — é com ele que o webhook do canal casa a conversa.
func chatwootSourceID(targetID string, isGroup bool) string {
	if isGroup {
		return targetID
	}
	sep := strings.Index(targetID, "@")
	if sep < 0 || !strings.EqualFold(targetID[sep+1:], "s.whatsapp.net") {
		return targetID
	}
	user := jidUserPart(targetID[:sep])
	if user == "" {
		return targetID
	}
	for _, r := range user {
		if r < '0' || r > '9' {
			return targetID
		}
	}
	return user
}

func shouldUpdateContactName(contact *ContactLookup, targetID string) bool {
	if contact == nil {
		return false
	}

	current := strings.ToLower(strings.TrimSpace(contact.Name))
	if current == "" {
		return true
	}

	target := strings.ToLower(strings.TrimSpace(targetID))
	user := strings.ToLower(jidUserPart(targetID))
	phone := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(contact.PhoneNumber)), "+")

	if current == target || (user != "" && current == user) {
		return true
	}
	if phone != "" && (current == phone || current == "+"+phone) {
		return true
	}
	if strings.Contains(current, "@s.whatsapp.net") || strings.Contains(current, "@lid") {
		return true
	}

	return false
}

func (s *Service) getAvatarURL(waClient *whatsmeow.Client, jid string) string {
	if waClient == nil {
		return ""
	}

	// Normaliza o JID para remover sufixos de dispositivos (ex: :1@s.whatsapp.net -> @s.whatsapp.net)
	// Isso garante que o prefixo do arquivo seja sempre consistente
	jidClean := jid
	if strings.Contains(jidClean, ":") {
		parts := strings.Split(jidClean, ":")
		if len(parts) > 1 {
			domainParts := strings.Split(parts[1], "@")
			if len(domainParts) > 1 {
				jidClean = parts[0] + "@" + domainParts[1]
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	parsed, ok := utils.ParseJID(jidClean)
	if !ok {
		return ""
	}

	fmt.Printf("[Chatwoot] Fetching profile picture for: %s\n", jidClean)
	info, err := waClient.GetProfilePictureInfo(ctx, parsed, nil)
	if err != nil {
		fmt.Printf("[Chatwoot] WhatsApp error fetching photo for %s: %v\n", jidClean, err)
		return ""
	}
	if info == nil || info.URL == "" {
		fmt.Printf("[Chatwoot] No profile picture found on WhatsApp for: %s\n", jidClean)
		return ""
	}

	waURL := info.URL

	// Se o Minio estiver habilitado, baixamos a imagem e enviamos para o nosso S3
	if s.MediaStorage != nil {
		fmt.Printf("[Chatwoot] Downloading avatar from WA: %s\n", jidClean)
		resp, err := http.Get(waURL)
		if err != nil {
			fmt.Printf("[Chatwoot] Error downloading WA photo for %s: %v\n", jidClean, err)
			return waURL
		}
		defer resp.Body.Close()

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return waURL
		}

		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "image/jpeg"
		}

		// Limpa qualquer avatar antigo deste JID antes de subir o novo
		prefixToClean := fmt.Sprintf("avatar_%s", strings.ReplaceAll(jidClean, "@", "_"))
		_ = s.MediaStorage.DeleteByPrefix(ctx, prefixToClean)

		// Nome único com timestamp para forçar atualização no Chatwoot (Bypass de Cache)
		fileName := fmt.Sprintf("%s_%d.jpg", prefixToClean, time.Now().Unix())

		fmt.Printf("[Chatwoot] Uploading avatar to Minio: %s\n", fileName)
		s3URL, err := s.MediaStorage.Store(ctx, data, fileName, contentType)
		if err != nil {
			fmt.Printf("[Chatwoot] Minio error for %s: %v\n", jidClean, err)
			return waURL
		}

		fmt.Printf("[Chatwoot] Avatar ready: %s\n", s3URL)
		return s3URL
	}

	return waURL
}

// maxMessageUnwrapDepth limita quantas camadas de wrapper são atravessadas ao
// desembrulhar uma mensagem, evitando recursão infinita em aninhamentos inválidos.
const maxMessageUnwrapDepth = 8

// messageWrapper mapeia um campo do protobuf Message que embrulha outro Message.
// No whatsmeow quase todos esses wrappers usam o tipo FutureProofMessage.
type messageWrapper struct {
	// name e o campo do protobuf, usado no log de diagnostico.
	name string
	// forwarded marca o wrapper que significa "mensagem encaminhada". Apenas o
	// botForwardedMessage qualifies; os demais sao so transporte e nao recebem o
	// prefixo "\u21aa" (o sinal real vem de isForwardedContext).
	forwarded bool
	inner     func(*waE2E.Message) *waE2E.Message
}

// messageWrappers lista os wrappers que a integração sabe desembrulhar.
//
// O whatsmeow (types/events.Message.UnwrapRaw) só desembrulha deviceSent, botInvoke,
// ephemeral, viewOnce, viewOnceV2, viewOnceV2Extension, lottieSticker,
// documentWithCaption e edited. Wrappers mais novos — como o botForwardedMessage —
// continuam embrulhados; sem desembrulhá-los o conteúdo cai no fallback
// "[Mensagem de tipo não identificado]".
//
// Wrappers de status/enquete (groupStatus, statusAddYours, pollCreationMessageV4,
// botTask, pollCreationOptionImage...) ficam de fora de propósito para não
// injetar tráfego de status no Chatwoot.
//
// Só o botForwardedMessage marca forwarded: os demais são apenas transporte, e
// quem de fato identifica uma encaminhada é o ContextInfo.isForwarded da mensagem
// de conteúdo (ver isForwardedContext). Em particular spoilerMessage é o wrapper
// de conteúdo escondido do WhatsApp e não tem relação com encaminhamento.
var messageWrappers = []messageWrapper{
	{name: "deviceSentMessage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetDeviceSentMessage().GetMessage() }},
	{name: "viewOnceMessage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetViewOnceMessage().GetMessage() }},
	{name: "viewOnceMessageV2", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetViewOnceMessageV2().GetMessage() }},
	{name: "viewOnceMessageV2Extension", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetViewOnceMessageV2Extension().GetMessage() }},
	{name: "ephemeralMessage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetEphemeralMessage().GetMessage() }},
	{name: "documentWithCaptionMessage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetDocumentWithCaptionMessage().GetMessage() }},
	{name: "lottieStickerMessage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetLottieStickerMessage().GetMessage() }},
	{name: "botInvokeMessage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetBotInvokeMessage().GetMessage() }},
	{name: "groupMentionedMessage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetGroupMentionedMessage().GetMessage() }},
	{name: "statusMentionMessage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetStatusMentionMessage().GetMessage() }},
	{name: "eventCoverImage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetEventCoverImage().GetMessage() }},
	{name: "newsletterAdminProfileMessage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetNewsletterAdminProfileMessage().GetMessage() }},
	{name: "newsletterAdminProfileMessageV2", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetNewsletterAdminProfileMessageV2().GetMessage() }},
	{name: "questionMessage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetQuestionMessage().GetMessage() }},
	{name: "questionReplyMessage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetQuestionReplyMessage().GetMessage() }},
	{name: "associatedChildMessage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetAssociatedChildMessage().GetMessage() }},
	{name: "limitSharingMessage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetLimitSharingMessage().GetMessage() }},
	{name: "botForwardedMessage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetBotForwardedMessage().GetMessage() }, forwarded: true},
	{name: "spoilerMessage", inner: func(m *waE2E.Message) *waE2E.Message { return m.GetSpoilerMessage().GetMessage() }},
}

// innerMessage devolve a mensagem embrulhada pelo primeiro wrapper presente em msg,
// ou nil quando msg ja e a mensagem de conteudo. Devolve tambem o nome do campo
// que casou, para o log de diagnostico.
func innerMessage(msg *waE2E.Message) (*waE2E.Message, string, bool) {
	if msg == nil {
		return nil, "", false
	}
	for _, w := range messageWrappers {
		if inner := w.inner(msg); inner != nil {
			return inner, w.name, w.forwarded
		}
	}
	return nil, "", false
}

// unwrapMessageContent desce nos wrappers ate a mensagem de conteudo real e
// informa se atravessou um wrapper de "encaminhada". Registra no log quais
// wrappers foram atravessados, para diagnosticar wrappers novos do WhatsApp.
func unwrapMessageContent(msg *waE2E.Message) (*waE2E.Message, bool) {
	forwarded := false
	traversed := ""
	for depth := 0; depth < maxMessageUnwrapDepth; depth++ {
		inner, name, isForwarded := innerMessage(msg)
		if inner == nil {
			break
		}
		msg = inner
		if isForwarded {
			forwarded = true
		}
		if traversed == "" {
			traversed = name
		} else {
			traversed += ">" + name
		}
	}
	if traversed != "" {
		fmt.Printf("[Chatwoot] unwrap: wrapper=%s forwarded=%v ctxForwarded=%v\n",
			traversed, forwarded, isForwardedContext(msg))
	}
	return msg, forwarded
}

// messageContextInfo devolve o ContextInfo da mensagem de conteudo, olhando todos
// os tipos que carregam um. Devolve nil quando a mensagem nao tem contexto.
func messageContextInfo(msg *waE2E.Message) *waE2E.ContextInfo {
	if msg == nil {
		return nil
	}
	switch {
	case msg.GetExtendedTextMessage() != nil:
		return msg.GetExtendedTextMessage().GetContextInfo()
	case msg.GetImageMessage() != nil:
		return msg.GetImageMessage().GetContextInfo()
	case msg.GetVideoMessage() != nil:
		return msg.GetVideoMessage().GetContextInfo()
	case msg.GetAudioMessage() != nil:
		return msg.GetAudioMessage().GetContextInfo()
	case msg.GetDocumentMessage() != nil:
		return msg.GetDocumentMessage().GetContextInfo()
	case msg.GetStickerMessage() != nil:
		return msg.GetStickerMessage().GetContextInfo()
	case msg.GetLocationMessage() != nil:
		return msg.GetLocationMessage().GetContextInfo()
	case msg.GetLiveLocationMessage() != nil:
		return msg.GetLiveLocationMessage().GetContextInfo()
	case msg.GetContactMessage() != nil:
		return msg.GetContactMessage().GetContextInfo()
	}
	return nil
}

// debugForward liga o log de diagnóstico do sinal de "encaminhada". Fica desligado
// por padrão porque escreve várias linhas por mensagem; ligue com
// CHATWOOT_DEBUG_FORWARD=1 ao investigar um tipo de conteúdo que não está
// recebendo a seta, para ver o payload real que o WhatsApp mandou.
var debugForward = os.Getenv("CHATWOOT_DEBUG_FORWARD") == "1"

// logForwardDiagnostics imprime o estado do ContextInfo de cada mensagem recebida.
//
// O WhatsApp sinaliza "encaminhada" em campos diferentes conforme o tipo de
// conteúdo e a versão do aplicativo, e o nome do campo varia (isForwarded,
// forwardingScore, forwardOrigin, ...). Em vez de chutar um campo, o log imprime
// TODOS os campos preenchidos da protobuf, para o sinal nunca depender de
// suposição: quando a seta não aparece, o log mostra exatamente o que chegou.
func logForwardDiagnostics(evt *events.Message, msg *waE2E.Message, isForwarded bool) {
	if !debugForward {
		return
	}
	kind := contentKind(msg)
	ctx := messageContextInfo(msg)
	fmt.Printf("[Chatwoot] fwd-diag: kind=%s seta=%v ctx=%v comment=%q\n",
		kind, isForwarded, ctx != nil, commentOf(msg))
	if ctx != nil {
		fmt.Printf("[Chatwoot] fwd-diag-ctx: {%s}\n", dumpProtoPreenchido(ctx))
	} else {
		fmt.Printf("[Chatwoot] fwd-diag-ctx: <ausente>\n")
	}
	if loc := msg.GetLocationMessage(); loc != nil {
		fmt.Printf("[Chatwoot] fwd-diag-loc: {%s}\n", dumpProtoPreenchido(loc))
	}
	fmt.Printf("[Chatwoot] fwd-diag-msg: {%s}\n", dumpCamposTopo(msg))
	_ = evt
}

// dumpProtoPreenchido lista TODOS os campos preenchidos de uma mensagem
// protobuf, com o valor. Como o ContextInfo tem mais de 60 campos e o WhatsApp
// muda o campo usado para marcar "encaminhada" conforme o tipo de conteudo e a
// versao do app, enumerar campos na mao erra: aqui o dump e generico e mostra
// exatamente o que chegou, inclusive campo que ninguem pensou em olhar.
func dumpProtoPreenchido(m proto.Message) string {
	if m == nil {
		return ""
	}
	r := m.ProtoReflect()
	if !r.IsValid() {
		return ""
	}
	campos := make([]string, 0, 8)
	r.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		campos = append(campos, fmt.Sprintf("%s=%s", fd.Name(), dumpValorProto(fd, v)))
		return true
	})
	return strings.Join(campos, " ")
}

// dumpCamposTopo lista os campos de primeiro nivel preenchidos da Message, para
// revelar embrulho (wrapper) que o unwrap nao conhecesse.
func dumpCamposTopo(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	return dumpProtoPreenchido(msg)
}

// dumpValorProto formata um valor protobuf de forma compacta e segura.
func dumpValorProto(fd protoreflect.FieldDescriptor, v protoreflect.Value) string {
	switch {
	case fd.IsMap():
		return fmt.Sprintf("mapa(%d)", v.Map().Len())
	case fd.IsList():
		l := v.List()
		if fd.Kind() == protoreflect.MessageKind && l.Len() > 0 {
			return fmt.Sprintf("lista(%d)[%s]", l.Len(), dumpProtoPreenchido(l.Get(0).Message().Interface()))
		}
		return fmt.Sprintf("lista(%d)", l.Len())
	}
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return "presente"
	case protoreflect.BytesKind:
		return fmt.Sprintf("bytes(%d)", len(v.Bytes()))
	case protoreflect.StringKind:
		s := v.String()
		if len(s) > 60 {
			s = s[:60] + "..."
		}
		return strconv.Quote(s)
	case protoreflect.EnumKind:
		ev := fd.Enum().Values().ByNumber(v.Enum())
		if ev != nil {
			return fmt.Sprintf("%s(%d)", ev.Name(), v.Enum())
		}
		return strconv.Itoa(int(v.Enum()))
	default:
		return v.String()
	}
}

// contentKind nomeia o tipo de conteúdo presente na mensagem, para o log.
func contentKind(msg *waE2E.Message) string {
	switch {
	case msg == nil:
		return "nil"
	case msg.GetExtendedTextMessage() != nil:
		return "text"
	case msg.GetImageMessage() != nil:
		return "image"
	case msg.GetVideoMessage() != nil:
		return "video"
	case msg.GetAudioMessage() != nil:
		return "audio"
	case msg.GetDocumentMessage() != nil:
		return "document"
	case msg.GetStickerMessage() != nil:
		return "sticker"
	case msg.GetLocationMessage() != nil:
		return "location"
	case msg.GetLiveLocationMessage() != nil:
		return "livelocation"
	case msg.GetContactMessage() != nil:
		return "contact"
	case msg.GetReactionMessage() != nil:
		return "reaction"
	case msg.GetEncReactionMessage() != nil:
		return "encreaction"
	case msg.GetProtocolMessage() != nil:
		return "protocol"
	}
	return "outro"
}

// commentOf devolve o comentário/legenda, que em conteúdo reencaminhado
// carrega o texto original de quem enviou.
func commentOf(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	switch {
	case msg.GetLocationMessage() != nil:
		return msg.GetLocationMessage().GetComment()
	case msg.GetImageMessage() != nil:
		return msg.GetImageMessage().GetCaption()
	case msg.GetVideoMessage() != nil:
		return msg.GetVideoMessage().GetCaption()
	case msg.GetDocumentMessage() != nil:
		return msg.GetDocumentMessage().GetCaption()
	}
	return ""
}

// isForwardedContext informa se o WhatsApp marcou a mensagem como encaminhada.
//
// Encaminhadas comuns (foto, texto, video, audio, figurinha, contato) nao usam
// nenhum wrapper: o sinal esta no ContextInfo da mensagem de conteudo, e o
// WhatsApp preenche isForwarded (verificado em producao: chega com
// isForwarded=true e forwardingScore=1 juntos). Os dois campos sao aceitos aqui.
//
// Localizacao e um caso a parte e NAO da para marcar: verificando o payload
// bruto, uma localizacao encaminhada chega sem ContextInfo nenhum — a
// LocationMessage so tem degreesLatitude, degreesLongitude e JPEGThumbnail, sem
// qualquer campo de origem. Nao existe sinal no payload, logo nenhuma logica
// consegue distinguir "localizacao encaminhada" de "localizacao enviada", e
// marcar todas seria errado.
//
// Por isso os wrappers de transporte como spoilerMessage nao viram seta: eles
// dizem apenas que a mensagem esta embrulhada, nao que foi encaminhada.
func isForwardedContext(msg *waE2E.Message) bool {
	ctx := messageContextInfo(msg)
	return ctx.GetIsForwarded() || ctx.GetForwardingScore() > 0
}

// applyDecorations acrescenta os prefixos de edicao e de encaminhada.
//
// E idempotente de proposito: ela e chamada duas vezes no fluxo (logo apos extrair
// o conteudo, e de novo antes do envio, porque os ramos de midia remontam o
// content do zero). Primeiro remove os prefixos ja existentes, em qualquer ordem,
// para nao duplicar quando um deles empurra o outro para fora da posicao 0.
func applyDecorations(content string, isEdit, isForwarded bool) string {
	content = strings.TrimPrefix(content, "↪ ")
	content = strings.TrimPrefix(content, "[Editado] ")
	content = strings.TrimPrefix(content, "↪ ")

	if isEdit {
		content = "[Editado] " + content
	}
	if isForwarded {
		content = "↪ " + content
	}
	return content
}

// senderHeader devolve o cabecalho de grupo do remetente, no formato
// "**telefone - Nome:**\n\n", ou string vazia fora de grupo / quando a mensagem
// e nossa. Formata o telefone quando ele parece ser BR (55 + DDD + 9 digitos).
func senderHeader(evt *events.Message, isGroup bool) string {
	if evt == nil || !isGroup || evt.Info.IsFromMe {
		return ""
	}
	name := evt.Info.PushName
	if name == "" {
		name = evt.Info.Sender.User
	}
	phone := evt.Info.Sender.User
	if strings.HasPrefix(phone, "55") && len(phone) >= 12 {
		return fmt.Sprintf("**%s - %s:**\n\n", phone, name)
	}
	return fmt.Sprintf("**%s:**\n\n", name)
}

// renderLocation monta o corpo de uma localizacao: rotulo + link do Google Maps.
func renderLocation(loc *waE2E.LocationMessage) string {
	mapsURL := fmt.Sprintf("https://maps.google.com/maps?q=%.6f,%.6f",
		loc.GetDegreesLatitude(), loc.GetDegreesLongitude())
	if name := loc.GetName(); name != "" {
		return fmt.Sprintf("\U0001F4CD *%s*\n%s", name, mapsURL)
	}
	return fmt.Sprintf("\U0001F4CD Localiza\u00e7\u00e3o\n%s", mapsURL)
}

// extractMessageContentFrom extrai o conteudo de uma mensagem ja desembrulhada.
func extractMessageContentFrom(msg *waE2E.Message, waClient *whatsmeow.Client) string {
	if msg == nil {
		return ""
	}
	if msg.ProtocolMessage != nil {
		if msg.ProtocolMessage.EditedMessage != nil {
			return "[Editado] " + extractMessageContentDirect(msg.ProtocolMessage.EditedMessage, waClient)
		}

		switch msg.ProtocolMessage.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			return "Esta mensagem foi excluída"
		case waE2E.ProtocolMessage_EPHEMERAL_SETTING:
			return "[Configuração de mensagens temporárias atualizada]"
		default:
			if msg.ProtocolMessage.GetKey() != nil && msg.ProtocolMessage.GetKey().GetID() != "" {
				return "Esta mensagem foi excluída"
			}
			return "[Mensagem de sistema]"
		}
	}
	return extractMessageContentDirect(msg, waClient)
}

func resolveMentions(text string, ctxInfo *waE2E.ContextInfo, waClient *whatsmeow.Client) string {
	if text == "" || ctxInfo == nil || len(ctxInfo.MentionedJID) == 0 || waClient == nil || waClient.Store == nil {
		return text
	}

	for _, jid := range ctxInfo.MentionedJID {
		parts := strings.Split(jid, "@")
		if len(parts) < 2 {
			continue
		}
		rawNum := parts[0]
		replacement := rawNum

		parsed, ok := utils.ParseJID(jid)
		if ok {
			contact, err := waClient.Store.Contacts.GetContact(context.Background(), parsed)
			if err == nil && contact.Found {
				if contact.PushName != "" {
					replacement = contact.PushName
				} else if contact.FullName != "" {
					replacement = contact.FullName
				}
			}
			if replacement == rawNum && parsed.Server == "lid" {
				// Try to get normal JID from LID representation
				alt, err := waClient.Store.GetAltJID(context.Background(), parsed)
				if err == nil && !alt.IsEmpty() {
					replacement = strings.Split(alt.String(), "@")[0]
				}
			}
		}

		text = strings.ReplaceAll(text, "@"+rawNum, "@"+replacement)
	}
	return text
}

func extractMessageContentDirect(msg *waE2E.Message, waClient *whatsmeow.Client) string {
	if msg == nil {
		return ""
	}
	if msg.GetConversation() != "" {
		return msg.GetConversation()
	}
	if msg.GetExtendedTextMessage() != nil {
		text := msg.GetExtendedTextMessage().GetText()
		ctxInfo := msg.GetExtendedTextMessage().GetContextInfo()
		return resolveMentions(text, ctxInfo, waClient)
	}
	if msg.GetImageMessage() != nil {
		return "[Imagem]"
	}
	if msg.GetVideoMessage() != nil {
		return "[Video]"
	}
	if msg.GetAudioMessage() != nil {
		return "[Áudio]"
	}
	if msg.GetDocumentMessage() != nil {
		return "[Documento]"
	}
	if msg.GetStickerMessage() != nil {
		return "[Figurinha]"
	}
	if msg.GetLocationMessage() != nil {
		return "[Localização]"
	}
	if msg.GetContactMessage() != nil {
		return formatContactMessage(msg.GetContactMessage())
	}
	if msg.GetContactsArrayMessage() != nil {
		return formatContactsArrayMessage(msg.GetContactsArrayMessage())
	}
	if msg.GetReactionMessage() != nil {
		return "Reagiu: " + msg.GetReactionMessage().GetText()
	}
	if msg.GetEncReactionMessage() != nil {
		return "Reagiu (Criptografado)"
	}
	if msg.GetPollCreationMessage() != nil {
		return "[Enquete] " + msg.GetPollCreationMessage().GetName()
	}
	if msg.GetPollUpdateMessage() != nil {
		return "[Voto em Enquete]"
	}
	if msg.GetButtonsResponseMessage() != nil {
		return msg.GetButtonsResponseMessage().GetSelectedDisplayText()
	}
	if msg.GetListResponseMessage() != nil {
		return msg.GetListResponseMessage().GetTitle()
	}
	if msg.GetInteractiveResponseMessage() != nil {
		return msg.GetInteractiveResponseMessage().GetBody().GetText()
	}
	return "[Mensagem de tipo não identificado]"
}

// formatContactMessage formata um contato único (vCard) recebido do WhatsApp,
// extraindo o nome exibido e os números de telefone presentes no vCard.
func formatContactMessage(cm *waE2E.ContactMessage) string {
	if cm == nil {
		return "[Contato]"
	}
	displayName := strings.TrimSpace(cm.GetDisplayName())
	numbers := extractPhoneNumbersFromVCard(cm.GetVcard())
	return mountContactText(displayName, numbers)
}

// formatContactsArrayMessage formata um lote de contatos (vCards) recebido do WhatsApp.
func formatContactsArrayMessage(cam *waE2E.ContactsArrayMessage) string {
	if cam == nil {
		return "[Contato]"
	}
	contacts := cam.GetContacts()
	if len(contacts) == 0 {
		return formatContactMessage(&waE2E.ContactMessage{DisplayName: cam.DisplayName})
	}
	var parts []string
	for _, c := range contacts {
		displayName := strings.TrimSpace(c.GetDisplayName())
		numbers := extractPhoneNumbersFromVCard(c.GetVcard())
		parts = append(parts, mountContactText(displayName, numbers))
	}
	return "[Contatos]\n" + strings.Join(parts, "\n")
}

// mountContactText monta o texto legível do contato: nome (quando existir)
// seguido do(s) número(s). Fallback para "[Contato]" se nada for extraído.
func mountContactText(displayName string, numbers []string) string {
	if displayName == "" && len(numbers) == 0 {
		return "[Contato]"
	}
	var sb strings.Builder
	sb.WriteString("[Contato]")
	if displayName != "" {
		sb.WriteString(" " + displayName)
	}
	if len(numbers) > 0 {
		sb.WriteString("\n" + strings.Join(numbers, "\n"))
	}
	return sb.String()
}

// extractPhoneNumbersFromVCard extrai os números de telefone (TEL) de um vCard.
// O WhatsApp grava o número no formato waid=<numero> ou TEL;type=...:<numero>,
// podendo vir com prefixo de grupo (ex.: ITEM1.TEL;type=...;waid=...).
func extractPhoneNumbersFromVCard(vcard string) []string {
	if vcard == "" {
		return nil
	}
	var numbers []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(vcard, "\n") {
		line = strings.TrimSpace(line)
		if !isVCardTelLine(line) {
			continue
		}
		// Formatos possíveis:
		//   ITEM1.TEL;type=CELL;type=VOICE;waid=558589605635:+55 85 98960-5635
		//   TEL;type=CELL;waid=558589605635:+55 85 98960-5635
		//   TEL:+55 85 98960-5635
		lower := strings.ToLower(line)
		num := ""
		if idx := strings.Index(lower, "waid="); idx >= 0 {
			rest := line[idx+len("waid="):]
			if colon := strings.Index(rest, ":"); colon >= 0 {
				rest = rest[:colon]
			}
			num = strings.TrimSpace(rest)
		} else if colon := strings.Index(line, ":"); colon >= 0 {
			num = strings.TrimSpace(line[colon+1:])
		}
		if num == "" {
			continue
		}
		// Normaliza: remove espaços, traços e parênteses
		clean := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(num)
		if !seen[clean] {
			seen[clean] = true
			numbers = append(numbers, num)
		}
	}
	return numbers
}

// isVCardTelLine indica se a linha do vCard é uma propriedade TEL, aceitando
// prefixos de grupo (ex.: ITEM1.TEL, item2.tel) usados por celulares/Android.
func isVCardTelLine(line string) bool {
	upper := strings.ToUpper(strings.TrimSpace(line))
	if upper == "" {
		return false
	}
	// Isola a propriedade (parte antes de ':' e de ';')
	prop := upper
	if idx := strings.IndexAny(prop, ";:"); idx >= 0 {
		prop = prop[:idx]
	}
	return prop == "TEL" || strings.HasSuffix(prop, ".TEL")
}

func extractEditedContent(evt *events.Message, waClient *whatsmeow.Client) string {
	if evt.RawMessage == nil {
		return ""
	}
	msg := evt.RawMessage
	if msg.ProtocolMessage == nil || msg.ProtocolMessage.EditedMessage == nil {
		return ""
	}
	return extractMessageContentDirect(msg.ProtocolMessage.EditedMessage, waClient)
}
