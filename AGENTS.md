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

### `source_id` do contact_inbox e disparo em massa (Campaign)

Este é o ponto mais frágil da integração. Leia antes de mexer em
`CreateContact`/`CreateConversation`.

**Convenção vigente:** o `source_id` do `contact_inbox` deve ser

| caso | `source_id` | `identifier` do contato |
|---|---|---|
| contato individual (`s.whatsapp.net`) | **só os dígitos** (`558589605635`) | JID (`558589605635@s.whatsapp.net`) |
| grupo (`@g.us`) / LID (`@lid`) | JID completo | JID completo |

Derivado por `chatwootSourceID()` (`internal/chatwoot/service.go`) e aplicado
num ponto único. **O `targetID` (identifier) NÃO deve ser alterado** — a busca
de contatos e a lógica do 9º dígito dependem dele.

**Por que os dígitos:** o disparo em massa (Campaign/Kanban do fork AstraChat)
é baseado em telefone e resolve a conversa pelo `source_id`. Em inbox
`Channel::Api`, `CampaignSender#candidate_source_ids` retorna só
`[phone.gsub(/\D/,'')]`. Com o JID no `source_id`, a campanha não acha o
`contact_inbox` e cria outro → **nova conversa duplicada**. Com dígitos, ela
acha e cai no `build_conversation`, que reabre a conversa existente.

**Fatos do Chatwoot (v4.17.0-1, fork AstraChat) que sustentam isso:**

- `ContactInboxBuilder#generate_source_id` → para `Channel::Api` o Chatwoot
  gera **`SecureRandom.uuid`**, não telefone. Ou seja, "source_id = telefone"
  é uma convenção **da nossa integração**, não do Chatwoot.
- A API **não expõe update de `contact_inbox`** (`contact_inboxes_controller`
  só tem `filter`; `contacts_controller` não aceita `source_id` no PUT).
  Portanto o `source_id` só pode ser definido **na criação** — por isso
  mandamos `source_id` no `POST /contacts` **e** no `POST /conversations`.
- `lock_to_single_conversation = true` na inbox → `ConversationBuilder`
  reaproveita `contact_inbox.conversations.last`, e o `POST /conversations`
  acha o `contact_inbox` pelo `source_id`. Com `source_id` único e estável
  por contato, nasce **1 contact_inbox por contato**.
- Apagar um contato apaga conversas e `contact_inboxes`
  (`dependent: :destroy_async`) **e as etiquetas** (`acts_as_taggable_on`) —
  ao recriar o contato é preciso **reaplicar a etiqueta**.
- Se houver corrida na deleção assíncrona, `ContactInboxBuilder#update_old_contact_inbox`
  renomeia o `source_id` antigo para um valor aleatório (se auto-protege).
- `normalize_phone` da campanha é só `to_s.gsub(/\D/,'')` — **não mexe no 9º
  dígito nem no código de país**. Logo o **CSV precisa do número em E.164 com
  código de país** (`558589605635`). Sem o `55`, a campanha cria um contato com
  outro número e, quando o cliente responde, o evo-go não acha e abre uma
  segunda conversa.
- O evo-go reabre conversa resolvida: `GetConversations` (`client.go`) usa
  `GET /contacts/:id/conversations`, que vem `order(last_activity_at: :desc)`
  **sem filtro de status**, e devolve a primeira da inbox.

**⚠️ Pendência do lado Chatwoot (não corrigir aqui):** contatos criados
**nativamente** no Chatwoot (UI, outras integrações) recebem `source_id` UUID,
e a campanha só procura dígitos. Se esse contato falar no WhatsApp, o evo-go
cria um `contact_inbox` de dígitos **adicional** — inofensivo (o de dígitos é
o canônico para a campanha), mas o extra só sai com patch no Chatwoot. Vale
lembrar que o fork AstraChat é **bytecode YARB**: se for necessário patchar,
usar um initializer que reabre a classe (não editar o `.yarb`).

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
7. **Nunca voltar a passar `targetID` como `source_id`.** Sempre usar
   `chatwootSourceID(targetID, isGroup)` em `CreateContact` **e** em
   `CreateConversation`, senão a campanha em massa volta a duplicar conversas.
8. Ao extrair telefone de vCard, a propriedade pode vir como `ITEM1.TEL` /
   `item2.tel` (formato Android/WhatsApp), não só `TEL`. Por isso o parser usa
   `isVCardTelLine()` (aceita propriedade terminada em `.TEL`) — não trocar de
   volta para `strings.HasPrefix(line, "TEL")`, que quebra no celular.

## 5. Build/teste rápido

Go **não está instalado no host** — compilar via container:

```bash
docker run --rm -v /root/evolution-go:/build -w /build golang:1.25.0-alpine \
  sh -c "apk add --no-cache git build-base libjpeg-turbo-dev libwebp-dev \
  && go build ./... && go vet ./..."
```

```bash
go build ./...          # compilar
go vet ./...            # lint estático
# binário principal:
go build -o evolution-go ./cmd/evolution-go
# imagem (versionar sempre):
docker build --build-arg VERSION=<x.y.z> -t marcelolimajw/evolution-go-chatwoot:<x.y.z> .
```

Não há suíte de testes automatizados da integração Chatwoot; validação é manual:
- mídia: enviar áudio/vídeo/imagem de um celular para a instância conectada e
  conferir entrega no painel + playback via Dashboard Script;
- resposta/deleção: responder/deletar do Chatwoot conferindo reflexo no WhatsApp;
- vCard: enviar um contato de um celular e conferir que chega **nome e número**;
- disparo em massa: criar campanha (por **etiqueta** ou **CSV** com número em
  E.164) e conferir que a mensagem cai na conversa **existente**, sem criar
  conversa nova.

Chatwoot local de desenvolvimento: banco `postgres` container, `chatwoot` db
(`docker exec postgres psql -U postgres -d chatwoot`). Comandos úteis de
inspeção: `contact_inboxes.source_id` (tem índice único `(inbox_id,
source_id)`), `conversations.contact_inbox_id`, `kanban_campaign_contacts.phone`.
O código do Chatwoot está em `/app` no container `chatwoot`; o fork AstraChat
entrega lógica em **bytecode YARB** — inspecione com `strings` ou
`RubyVM::InstructionSequence.load_from_binary(File.binread(x.yarb)).disasm`.

## 6. Referências

- Upstream Go: https://github.com/EvolutionAPI/evolution-go
- Referência JS (comparação de comportamento): https://github.com/evolution-foundation/evolution-api
  - `src/api/integrations/chatbot/chatwoot/services/chatwoot.service.ts`
- Bugs do Chatwoot que motivam o modo híbrido:
  https://github.com/chatwoot/chatwoot/issues/14511 , /issues/14644
- Formato Dashboard Scripts (comunidade): https://github.com/bode327/DashBoard-Script
