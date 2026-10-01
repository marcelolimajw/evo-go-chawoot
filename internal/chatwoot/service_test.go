package chatwoot

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func testLocation() *waE2E.Message {
	return &waE2E.Message{
		LocationMessage: &waE2E.LocationMessage{
			DegreesLatitude:  proto.Float64(-3.788077),
			DegreesLongitude: proto.Float64(-38.601667),
			Name:             proto.String("Praça da Alegria"),
		},
	}
}

func forwarded(inner *waE2E.Message) *waE2E.Message {
	return &waE2E.Message{
		BotForwardedMessage: &waE2E.FutureProofMessage{Message: inner},
	}
}

// Regressão: localizações encaminhadas chegam dentro do wrapper botForwardedMessage.
// Sem desembrulhar, GetLocationMessage() do nível superior é nil e o conteúdo
// cai no fallback "[Mensagem de tipo não identificado]".
func TestUnwrapMessageContent_BotForwardedLocation(t *testing.T) {
	loc := testLocation()
	inner, isForwarded := unwrapMessageContent(forwarded(loc))

	if !isForwarded {
		t.Fatal("esperava isForwarded=true para wrapper botForwardedMessage")
	}
	if inner.GetLocationMessage() == nil {
		t.Fatal("esperava LocationMessage após desembrulhar botForwardedMessage")
	}
	if got := inner.GetLocationMessage().GetName(); got != "Praça da Alegria" {
		t.Fatalf("nome da localização inesperado: %q", got)
	}
}

func TestUnwrapMessageContent_NoWrapper(t *testing.T) {
	loc := testLocation()
	inner, isForwarded := unwrapMessageContent(loc)

	if isForwarded {
		t.Fatal("mensagem direta não deveria ser marcada como encaminhada")
	}
	if inner != loc {
		t.Fatal("mensagem sem wrapper deveria ser devolvida sem alteração")
	}
}

// Wrappers podem vir aninhados (ex.: encaminhada + visualização única).
func TestUnwrapMessageContent_Nested(t *testing.T) {
	msg := forwarded(&waE2E.Message{
		ViewOnceMessageV2: &waE2E.FutureProofMessage{Message: testLocation()},
	})
	inner, isForwarded := unwrapMessageContent(msg)

	if !isForwarded {
		t.Fatal("esperava isForwarded=true em wrapper aninhado")
	}
	if inner.GetLocationMessage() == nil {
		t.Fatal("esperava LocationMessage após desembrulhar wrappers aninhados")
	}
}

// Um wrapper sem Message interna não pode gerar panic.
func TestUnwrapMessageContent_EmptyWrapper(t *testing.T) {
	msg := &waE2E.Message{
		BotForwardedMessage: &waE2E.FutureProofMessage{},
	}
	inner, isForwarded := unwrapMessageContent(msg)

	if inner != msg {
		t.Fatal("wrapper vazio deveria ser devolvido sem alteração")
	}
	if isForwarded {
		t.Fatal("wrapper vazio não registra encaminhada")
	}
}

// Aninhamento patológico (ciclo) precisa respeitar o limite de profundidade.
func TestUnwrapMessageContent_DepthLimit(t *testing.T) {
	msg := &waE2E.Message{}
	msg.BotForwardedMessage = &waE2E.FutureProofMessage{Message: msg}

	done := make(chan struct{})
	go func() {
		defer close(done)
		inner, _ := unwrapMessageContent(msg)
		if inner == nil {
			t.Error("unwrapMessageContent devolveu nil")
		}
	}()
	<-done
}

func TestExtractMessageContentFrom_ForwardedLocation(t *testing.T) {
	msg, _ := unwrapMessageContent(forwarded(testLocation()))
	content := extractMessageContentFrom(msg, nil)

	if !strings.Contains(content, "[Localização]") {
		t.Fatalf("esperava conteúdo de localização, veio %q", content)
	}
	if strings.Contains(content, "não identificado") {
		t.Fatalf("mensagem encaminhada não deveria cair no fallback, veio %q", content)
	}
}

// Mídia encaminhada também precisa ser reconhecida (não só localização).
func TestExtractMessageContentFrom_ForwardedImage(t *testing.T) {
	msg, _ := unwrapMessageContent(forwarded(&waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/jpeg")},
	}))
	content := extractMessageContentFrom(msg, nil)

	if content != "[Imagem]" {
		t.Fatalf("esperava %q, veio %q", "[Imagem]", content)
	}
}

// ProtocolMessage continua sendo tratado antes de qualquer conteúdo.
func TestExtractMessageContentFrom_ProtocolMessage(t *testing.T) {
	tests := []struct {
		name string
		msg  *waE2E.Message
		want string
	}{
		{
			name: "revoke",
			msg:  &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum()}},
			want: "Esta mensagem foi excluída",
		},
		{
			name: "ephemeral setting",
			msg:  &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_EPHEMERAL_SETTING.Enum()}},
			want: "[Configuração de mensagens temporárias atualizada]",
		},
		{
			// Tipo desconhecido e sem Key: cai no branch de sistema.
			name: "mensagem de sistema",
			msg:  &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_Type(99).Enum()}},
			want: "[Mensagem de sistema]",
		},
		{
			// Tipo desconhecido mas com Key preenchida é tratada como exclusão.
			name: "tipo desconhecido com key",
			msg: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
				Type: waE2E.ProtocolMessage_Type(99).Enum(),
				Key:  &waCommon.MessageKey{ID: proto.String("ABC123")},
			}},
			want: "Esta mensagem foi excluída",
		},
		{
			name: "editado",
			msg: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
				EditedMessage: &waE2E.Message{Conversation: proto.String("texto editado")},
			}},
			want: "[Editado] texto editado",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractMessageContentFrom(tc.msg, nil); got != tc.want {
				t.Fatalf("esperava %q, veio %q", tc.want, got)
			}
		})
	}
}

func TestExtractMessageContentFrom_Nil(t *testing.T) {
	if got := extractMessageContentFrom(nil, nil); got != "" {
		t.Fatalf("esperava string vazia, veio %q", got)
	}
}

// Wrappers de transparência (não são "encaminhada") não devem marcar o balloon.
func TestUnwrapMessageContent_TransparencyWrappersNotForwarded(t *testing.T) {
	wrappers := map[string]*waE2E.Message{
		"viewOnceMessage": {
			ViewOnceMessage: &waE2E.FutureProofMessage{Message: testLocation()},
		},
		"ephemeralMessage": {
			EphemeralMessage: &waE2E.FutureProofMessage{Message: testLocation()},
		},
		"groupMentionedMessage": {
			GroupMentionedMessage: &waE2E.FutureProofMessage{Message: testLocation()},
		},
		"deviceSentMessage": {
			DeviceSentMessage: &waE2E.DeviceSentMessage{Message: testLocation()},
		},
	}

	for name, msg := range wrappers {
		t.Run(name, func(t *testing.T) {
			inner, isForwarded := unwrapMessageContent(msg)
			if isForwarded {
				t.Fatalf("%s não deveria marcar como encaminhada", name)
			}
			if inner.GetLocationMessage() == nil {
				t.Fatalf("%s deveria ter sido desembrulhado", name)
			}
		})
	}
}

// SpoilerMessage também representa "encaminhada" e marca o balloon.
func TestUnwrapMessageContent_SpoilerIsForwarded(t *testing.T) {
	msg := &waE2E.Message{
		SpoilerMessage: &waE2E.FutureProofMessage{Message: testLocation()},
	}
	inner, isForwarded := unwrapMessageContent(msg)

	if !isForwarded {
		t.Fatal("esperava isForwarded=true para spoilerMessage")
	}
	if inner.GetLocationMessage() == nil {
		t.Fatal("esperava LocationMessage após desembrulhar spoilerMessage")
	}
}
