# AGENTS.md — Contexto para Assistentes de IA

> Este documento existe para que qualquer LLM/agente entenda rapidamente o
> projeto, as customizações locais e as decisões técnicas antes de mexer no código.

## 1. O que é este projeto

**evolution-go**: API de WhatsApp escrita em Go (fork de
`EvolutionAPI/evolution-go`, baseada em whatsmeow), parte do ecossistema Evolution.
Expõe API REST + WebSocket/webhooks para gerenciar instâncias do WhatsApp
(conexão QR, envio/recebimento de mensagens e mídias).

Este repositório contém **customizações locais não presentes no upstream**,
cujo objetivo principal é uma **integração completa com Chatwoot**
(multi-instância, assinatura, mídia híbrida via Minio, supressão de eco,
mapeamento de respostas/deleções). As mudanças vivem principalmente em:

```
internal/chatwoot/           # núcleo da integração (service, client, models, manager)
internal/chatwoot/api/       # handler do webhook do Chatwoot + gestão multi-instância
pkg/storage/minio/           # storage S3/Minio usado no modo híbrido de mídia
manager/dist/chatwoot.html   # UI do manager para configurar instâncias
Extras/chatwoot/             # Dashboard Script (player de áudio/vídeo) + instruções
```

## 2. Integração com Chatwoot — como funciona

### Fluxo WhatsApp → Chatwoot (`internal/chatwoot/service.go`)

1. Recebe `*events.Message` do whatsmeow e resolve JID/LID para identificar o contato.
2. Cria/atualiza contato e conversa no Chatwoot (busca por identifier, fallback
   do 9º dígito BR, avatar re-hospedado no Minio).
3. Extrai texto/mídia. Mídias são baixadas pelo whatsmeow.
4. **Modo híbrido de mídia (importante — não remover):**
   - **Imagens/stickers/documentos**: enviados como **anexo nativo** multipart
     (`SendMessageWithAttachment`) + upload de backup no Minio em background.
   - **Áudios/vídeos**: enviados ao Minio e entregues ao Chatwoot como
     **mensagem de TEXTO** com rótulo + markdown `[ ](url-direta-do-minio)`,
     ex.: `▶️ **Mensagem de voz** [ ](https://minio/bucket/.../id.ogg)`.
5. Toda mensagem recebe `external_id = WAID:<msg-id>` (usado p/ dedupe e deleção).
6. Reações, mensagens citadas (`stanza_id` → `in_reply_to`), edições e REVOKES
   são mapeados (`mapping.go`: msgMappings WA↔Chatwoot persistido em JSON).

### Fluxo Chatwoot → WhatsApp (`internal/chatwoot/api/handler.go`)

- Webhook recebe `message_created` outgoing; ignora ecos (EchoCache por
  conteúdo+conversa, `external_id` prefixado `WAID:`), duplicatas e inbox errado.
- Com anexo: baixa a URL (`data_url`) e envia via `SendService.SendMediaUrl`
  (áudio→`audio.ogg`, imagem→`image.jpg`, vídeo→`video.mp4`).
- Texto: `SendService.SendText`, com suporte a resposta citada
  (`in_reply_to` → mapeamento reverso CW→WA) e assinatura opcional do agente.
- Deleção no Chatwoot (`message_updated` deleted) propaga para o WhatsApp.

### Configuração multi-instância

`manager.go` persiste configs por instância (URL, token, account_id, inbox_id,
assinatura) em `/app/dbdata/chatwoot_instances.json`; endpoints de gestão em
`internal/chatwoot/api/handler.go` (autenticados por `apikey == GlobalApiKey`);
fallback para envs `CHATWOOT_*`.

## 3. Player de áudio/vídeo no dashboard (`Extras/chatwoot/`)

**Problema:** no modo híbrido, áudios/vídeos chegam ao Chatwoot como texto +
link. O Chatwoot só renderiza botão play para **anexos reais**, então o play
depende de um script injetado no dashboard.

**Por que NÃO enviar áudio/vídeo como anexo nativo:** bugs abertos do Chatwoot
4.x — [#14511] (fechada sem fix) e [#14644] (aberta): race condition que faz o
primeiro load do anexo retornar 404 e o player travar em 00:00 até F5/trocar de
conversa. Confirmado até v4.16.x; PRs de correção (#13675/#14758) não mesclados.
O modo Minio + link direto contorna isso (URL pública permanente, sem ActiveStorage).

**Solução atual (validada funcionando):**

| Arquivo | Uso |
|---|---|
| `Extras/chatwoot/dashboard-script.js` | JS puro (referência/console) |
| `Extras/chatwoot/dashboard-script-colar.html` | **Arquivo para colar** no campo *Dashboard Scripts* |
| `Extras/chatwoot/dashboard-player.user.js` | Variante Tampermonkey (mesmo código + header userscript) |

Detalhes críticos:

- Instala-se em `https://<chatwoot>/super_admin/app_config?config=internal`,
  campo **Dashboard Scripts** (fork modificado, versão reporta `v18.0`).
- ⚠️ Esse campo espera blocos HTML `<script data-name="...">...</script>`
  (formato dos módulos da comunidade, ex.: `bode327/DashBoard-Script`).
  **Colar JS cru não executa.**
- O script observa o DOM (MutationObserver + varredura periódica), encontra
  âncoras/texto com URLs terminadas em extensões de mídia (.ogg/.oga/.opus/
  .mp3/.m4a/.wav/.aac/.mp4/.webm/.mov/.mkv/.avi) e injeta `<audio controls>` /
  `<video controls>` após o bloco `.prose` da mensagem. Anti-duplicação via
  WeakMap (âncora→player e host→URLs); re-injeta se o Vue re-renderizar.
- Não depende de classes internas do Chatwoot (resiste a updates).
- Limitação conhecida: OGG/Opus não toca em iOS/Safari (limitação do navegador;
  issue chatwoot #13210).

## 4. Convenções e decisões que devem ser preservadas

1. **Não trocar o modo híbrido por anexo nativo** para áudio/vídeo enquanto os
   bugs #14511/#14644 não tiverem fix oficial em release.
2. Comentários/logs do código de integração estão em **pt-BR** — manter o padrão.
3. `external_id`/`source_id = WAID:<id>` e o mapeamento em `mapping.go` são
   essenciais para deleção e replies — não quebrar.
4. Supressão de eco (EchoCache + filtro `WAID:`) evita loop WA↔CW — qualquer
   novo caminho de envio precisa registrar no EchoCache quando `IsFromMe`.
5. URLs do Minio são públicas e permanentes (`pkg/storage/minio/media_storage.go`)
   — o player conta com isso (sem query string/presign).
6. Ao alterar o player JS, manter os 3 arquivos de `Extras/chatwoot/`
   sincronizados (mesmo corpo; só muda o wrapper/header) e validar sintaxe
   (esprima/node --check).

## 5. Build/teste rápido

```bash
go build ./...          # compilar
go vet ./...            # lint estático
# binário principal:
go build -o evolution-go ./cmd/evolution-go
```

Não há suíte de testes automatizados da integração Chatwoot; validação é manual:
enviar áudio/vídeo/imagem de um celular para a instância conectada e conferir
entrega no painel + playback via Dashboard Script, e responder/deletar do
Chatwoot conferindo reflexo no WhatsApp.

## 6. Referências

- Upstream Go: https://github.com/EvolutionAPI/evolution-go
- Referência JS (comparação de comportamento): https://github.com/evolution-foundation/evolution-api
  - `src/api/integrations/chatbot/chatwoot/services/chatwoot.service.ts`
- Bugs do Chatwoot que motivam o modo híbrido:
  https://github.com/chatwoot/chatwoot/issues/14511 , /issues/14644
- Formato Dashboard Scripts (comunidade): https://github.com/bode327/DashBoard-Script
