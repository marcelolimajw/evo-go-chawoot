# Chatwoot Media Player (evolution-go)

> **Status: VALIDADO EM PRODUÇÃO** ✅ — instalado via campo *Dashboard Scripts*
> (`/super_admin/app_config?config=internal`) em Chatwoot modificado v18.0,
> com playback de áudio/vídeo funcionando.
>
> Contexto completo do projeto e das decisões: ver [`AGENTS.md`](../../AGENTS.md).

Restaura o botão de play de **áudio e vídeo** no dashboard do Chatwoot para as
mensagens enviadas pelo evolution-go em modo híbrido (upload Minio + link direto).

## Por que é necessário

- O evolution-go (modo Minio) envia áudio/vídeo ao Chatwoot como **mensagem de
  texto** contendo um link direto: `Mensagem de voz [ ](https://minio/.../audio.ogg)`.
  Isso foi feito para contornar o bug do Chatwoot 4.x em que anexos nativos
  chegam "travados" (00:00) até atualizar a página
  ([#14511](https://github.com/chatwoot/chatwoot/issues/14511),
  [#14644](https://github.com/chatwoot/chatwoot/issues/14644)).
- O Chatwoot **nunca** renderiza player para mensagens de texto, somente para
  anexos reais. Antigamente o botão de play vinha de um script injetado via
  *Integration Hooks* — recurso que foi **removido** no Chatwoot v4.x
  (a API de hooks hoje só aceita `app_id`, `inbox_id`, `status` e `settings`).
- Este script observa as bolhas de mensagem do dashboard, encontra links de
  mídia (`.ogg .oga .opus .mp3 .m4a .wav .aac .mp4 .webm .mov ...`) e injeta um
  `<audio controls>` / `<video controls>` nativo logo abaixo do texto.

É resistente a atualizações: não depende de classes CSS internas do Chatwoot,
apenas de `[data-message-id]` (presente desde versões antigas) e da extensão da URL.

## Instalação

### Opção A — Campo "Dashboard Scripts" (`/super_admin/app_config?config=internal`)

> **IMPORTANTE:** o campo "Dashboard Scripts" desses forks espera blocos HTML
> `<script>...</script>` (igual aos módulos da comunidade, ex.: `bode327/DashBoard-Script`).
> Colar JS **cru** não funciona.

1. Acesse como super admin: `https://SEU-CHATWOOT/super_admin/app_config?config=internal`
2. Localize o campo **Dashboard Scripts**
3. Cole o conteúdo completo de [`dashboard-script-colar.html`](./dashboard-script-colar.html)
   (ele já vem embrulhado em `<script data-name="Evolution-Go Media Player">...</script>`).
   - Se o campo já tiver outros scripts, cole este em uma nova linha abaixo deles.
4. Role até o fim da página e **Salve**
5. Recarregue o dashboard do agente (F5) e abra uma conversa com áudio/vídeo
6. No console (F12) deve aparecer: `[evolution-go] Chatwoot Media Player v1.1.0 ativo`

### Opção B — Tampermonkey/Violentmonkey (Chatwoot oficial)

1. Instale a extensão [Tampermonkey](https://www.tampermonkey.net/) no Chrome/Edge/Firefox.
2. Crie um novo script e cole o conteúdo de [`dashboard-player.user.js`](./dashboard-player.user.js).
3. No cabeçalho do script, troque:

   ```
   // @match https://SEU-CHATWOOT.EXEMPLO.COM/*
   ```

   pelo endereço real do seu Chatwoot.
4. Salve e recarregue o dashboard.

### Opção C — Injeção no reverse proxy (todos os agentes, sem extensão)

Se o Chatwoot fica atrás de nginx, basta injetar o script no HTML:

```nginx
location / {
    proxy_pass http://chatwoot:3000;

    sub_filter '</body>' '<script src="https://SEU-CDN/dashboard-player.js"></script></body>';
    sub_filter_once on;
    # necessário se o HTML vier comprimido do upstream:
    proxy_set_header Accept-Encoding "";
}
```

> Sirva o arquivo renomeado para `.js` (sem o cabeçalho `==UserScript==`,
> que é inofensivo e pode permanecer). Como o Chatwoot não envia CSP por
> padrão (`config/initializers/content_security_policy.rb` vem comentado),
> a injeção funciona sem ajustes extras.

## Status dos bugs de anexo travado (por que não usamos anexo nativo)

- [#14511](https://github.com/chatwoot/chatwoot/issues/14511) — fechada **sem correção**; reproduzido no v4.16.2 (jul/2026).
- [#14644](https://github.com/chatwoot/chatwoot/issues/14644) — aberta; race condition: o Chatwoot faz broadcast da mensagem via WebSocket antes do upload do arquivo terminar no storage.
- PRs de correção ([#13675](https://github.com/chatwoot/chatwoot/pull/13675), [#14758](https://github.com/chatwoot/chatwoot/pull/14758)) — ainda não mesclados.

Enquanto isso, enviar anexo nativo pela API continua gerando áudio/vídeo
travados em 00:00 até F5. O modo Minio + Dashboard Script evita o problema.

## Teste rápido (sem instalar nada)

1. Abra uma conversa com áudio/vídeo recebido pelo evolution-go.
2. Abra o DevTools (F12) → Console.
3. Cole o conteúdo de `dashboard-script.js` e Enter.
4. O player deve aparecer abaixo de cada mensagem de mídia.

## Notas

- Formatos `.ogg/.oga/.opus`: tocam em Chrome, Edge, Firefox e Android
  (desktop web). iOS/Safari não suporta OGG nativamente — limitação conhecida
  do próprio navegador ([issue #13210](https://github.com/chatwoot/chatwoot/issues/13210)).
- As URLs do Minio geradas pelo evolution-go são públicas e permanentes,
  então o playback continua funcionando mesmo depois de dias/semanas.
