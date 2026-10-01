package chatwoot

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
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

// SpoilerMessage é o wrapper de "conteúdo escondido" do WhatsApp, não de
// encaminhada: desembrulha, mas não pode marcar o balloon com "↪".
func TestUnwrapMessageContent_SpoilerIsNotForwarded(t *testing.T) {
	msg := &waE2E.Message{
		SpoilerMessage: &waE2E.FutureProofMessage{Message: testLocation()},
	}
	inner, isForwarded := unwrapMessageContent(msg)

	if isForwarded {
		t.Error("spoilerMessage não é encaminhada: isForwarded deveria ser false")
	}
	if inner.GetLocationMessage() == nil {
		t.Fatal("esperava LocationMessage após desembrulhar spoilerMessage")
	}
}

// --- Seta de encaminhada via ContextInfo (0.0.7) -------------------------
//
// Encaminhadas comuns (localização, foto, texto) NÃO usam nenhum wrapper: chegam
// como LocationMessage/ImageMessage/ExtendedTextMessage com
// ContextInfo.isForwarded = true. Era esse o sinal que faltava para o "↪"
// aparecer, já que o wrapper botForwardedMessage não é usado nesse fluxo.

func testForwardedLocation() *waE2E.Message {
	return &waE2E.Message{
		LocationMessage: &waE2E.LocationMessage{
			DegreesLatitude:  proto.Float64(-3.825716),
			DegreesLongitude: proto.Float64(-38.556443),
			ContextInfo:      &waE2E.ContextInfo{IsForwarded: proto.Bool(true)},
		},
	}
}

func TestIsForwardedContext_Location(t *testing.T) {
	if !isForwardedContext(testForwardedLocation()) {
		t.Error("localizacao com ContextInfo.isForwarded deveria ser encaminhada")
	}
}

func TestIsForwardedContext_Text(t *testing.T) {
	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:       proto.String("olá"),
			ContextInfo: &waE2E.ContextInfo{IsForwarded: proto.Bool(true)},
		},
	}
	if !isForwardedContext(msg) {
		t.Error("texto com ContextInfo.isForwarded deveria ser encaminhada")
	}
}

func TestIsForwardedContext_Image(t *testing.T) {
	msg := &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{
			Mimetype:   proto.String("image/jpeg"),
			ContextInfo: &waE2E.ContextInfo{IsForwarded: proto.Bool(true)},
		},
	}
	if !isForwardedContext(msg) {
		t.Error("foto com ContextInfo.isForwarded deveria ser encaminhada")
	}
}

func TestIsForwardedContext_NaoEncaminhada(t *testing.T) {
	cases := map[string]*waE2E.Message{
		"sem contextInfo": testLocation(),
		"contextInfo sem isForwarded": {
			LocationMessage: &waE2E.LocationMessage{
				ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("ABC")},
			},
		},
		"isForwarded false": {
			LocationMessage: &waE2E.LocationMessage{
				ContextInfo: &waE2E.ContextInfo{IsForwarded: proto.Bool(false)},
			},
		},
		"nil": nil,
	}
	for nome, msg := range cases {
		if isForwardedContext(msg) {
			t.Errorf("%s: nao deveria ser encaminhada", nome)
		}
	}
}

func TestIsForwardedContext_TextoDaPropriaPessoaNaoEhForward(t *testing.T) {
	// Resposta citada tem ContextInfo, mas não é encaminhamento.
	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text:       proto.String("resposta"),
			ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("XYZ")},
		},
	}
	if isForwardedContext(msg) {
		t.Error("resposta citada nao e encaminhamento")
	}
}

// --- Cabeçalho de grupo ---------------------------------------------------

func testGroupEvent(pushName, senderUser string, fromMe bool) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender:   types.JID{User: senderUser, Server: types.HiddenUserServer},
				Chat:     types.JID{User: "5511999999999", Server: types.GroupServer},
				IsFromMe: fromMe,
				IsGroup:  true,
			},
			PushName: pushName,
		},
	}
}

func TestSenderHeader_GrupoBR(t *testing.T) {
	evt := testGroupEvent("Marcelo", "558589605635", false)
	got := senderHeader(evt, true)
	want := "**558589605635 - Marcelo:**\n\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSenderHeader_GrupoNaoBR(t *testing.T) {
	evt := testGroupEvent("John", "447700900123", false)
	got := senderHeader(evt, true)
	want := "**John:**\n\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSenderHeader_SemPushNameUsaNumero(t *testing.T) {
	evt := testGroupEvent("", "558589605635", false)
	if got := senderHeader(evt, true); got != "**558589605635 - 558589605635:**\n\n" {
		t.Errorf("got %q", got)
	}
}

func TestSenderHeader_NaoGrupoEDaPropriaPessoa(t *testing.T) {
	evt := testGroupEvent("Marcelo", "558589605635", true)
	if got := senderHeader(evt, true); got != "" {
		t.Errorf("mensagem propria nao deve ter cabecalho, got %q", got)
	}
	if got := senderHeader(testGroupEvent("Marcelo", "558589605635", false), false); got != "" {
		t.Errorf("fora de grupo nao deve ter cabecalho, got %q", got)
	}
	if got := senderHeader(nil, true); got != "" {
		t.Errorf("evt nil nao deve ter cabecalho, got %q", got)
	}
}

// --- Localização: corpo + cabeçalho ---------------------------------------

func TestRenderLocation_ComNome(t *testing.T) {
	loc := testLocation().GetLocationMessage()
	want := "📍 *Praça da Alegria*\nhttps://maps.google.com/maps?q=-3.788077,-38.601667"
	if got := renderLocation(loc); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderLocation_SemNome(t *testing.T) {
	loc := &waE2E.LocationMessage{
		DegreesLatitude:  proto.Float64(-3.825716),
		DegreesLongitude: proto.Float64(-38.556443),
	}
	want := "📍 Localização\nhttps://maps.google.com/maps?q=-3.825716,-38.556443"
	if got := renderLocation(loc); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Regressão da conversa 167: o ramo de localização sobrescrevia content do zero
// e perdia o cabeçalho de grupo, enquanto os demais ramos de mídia o remontavam.
func TestLocalizacaoEmGrupoMontaCabecalhoECorpo(t *testing.T) {
	evt := testGroupEvent("Marcelo", "558589605635", false)
	loc := testLocation().GetLocationMessage()

	got := senderHeader(evt, true) + renderLocation(loc)
	want := "**558589605635 - Marcelo:**\n\n" +
		"📍 *Praça da Alegria*\nhttps://maps.google.com/maps?q=-3.788077,-38.601667"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- Decoração: [Editado] e ↪ --------------------------------------------

func TestApplyDecorations_Encaminhada(t *testing.T) {
	got := applyDecorations("📍 Localização\nhttps://maps", false, true)
	if !strings.HasPrefix(got, "↪ ") {
		t.Errorf("esperava prefixo ↪, got %q", got)
	}
}

func TestApplyDecorations_EdicaoEEncaminhada(t *testing.T) {
	got := applyDecorations("oi", true, true)
	if got != "↪ [Editado] oi" {
		t.Errorf("got %q, want %q", got, "↪ [Editado] oi")
	}
}

// Regressão: os ramos de mídia remontam content do zero, então applyDecorations
// é chamado duas vezes. Precisa ser idempotente para não duplicar os prefixos.
func TestApplyDecorations_Idempotente(t *testing.T) {
	uma := applyDecorations("corpo", true, true)
	duas := applyDecorations(uma, true, true)
	if uma != duas {
		t.Errorf("não idempotente: %q != %q", uma, duas)
	}
	if strings.Count(duas, "↪") != 1 || strings.Count(duas, "[Editado]") != 1 {
		t.Errorf("prefixos duplicados: %q", duas)
	}
}

// Composição final esperada para a localização encaminhada da conversa 167.
func TestComposicaoFinal_LocalizacaoEncaminhadaEmGrupo(t *testing.T) {
	evt := testGroupEvent("Marcelo", "558589605635", false)
	content := senderHeader(evt, true) + renderLocation(testForwardedLocation().GetLocationMessage())
	content = applyDecorations(content, false, isForwardedContext(testForwardedLocation()))

	want := "↪ **558589605635 - Marcelo:**\n\n" +
		"📍 Localização\nhttps://maps.google.com/maps?q=-3.825716,-38.556443"
	if content != want {
		t.Errorf("got %q, want %q", content, want)
	}
}

// Localização direta (não encaminhada) não recebe seta, mas mantém o cabeçalho.
func TestComposicaoFinal_LocalizacaoDiretaNaoRecebeSeta(t *testing.T) {
	evt := testGroupEvent("Marcelo", "558589605635", false)
	content := senderHeader(evt, true) + renderLocation(testLocation().GetLocationMessage())
	content = applyDecorations(content, false, isForwardedContext(testLocation()))

	if strings.Contains(content, "↪") {
		t.Errorf("localização direta não deve ter ↪: %q", content)
	}
	if !strings.HasPrefix(content, "**558589605635 - Marcelo:**\n\n") {
		t.Errorf("localização direta deve manter o cabeçalho: %q", content)
	}
}
