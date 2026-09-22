// ==UserScript==
// @name         Evolution-Go | Chatwoot Media Player
// @namespace    evolution-go-chatwoot-player
// @version      1.1.0
// @description  Renderiza player nativo de audio/video nas mensagens enviadas pelo evolution-go (links diretos Minio/S3). Restaura o botao de play que o Chatwoot 4.x nao renderiza para mensagens de texto.
// @author       evolution-go
// @match        https://SEU-CHATWOOT.EXEMPLO.COM/*
// @run-at       document-idle
// @grant        none
// ==/UserScript==

/*
 * ============================================================
 * Evolution-Go | Chatwoot Media Player — DASHBOARD SCRIPT
 * ============================================================
 * Cole este código inteiro no campo "Dashboard Script" do seu
 * Chatwoot (versão modificada) ou sirva como <script src>.
 *
 * O que faz:
 *   O evolution-go (modo Minio) envia áudio/vídeo como mensagem
 *   de texto contendo um link direto:
 *     "▶️ Mensagem de voz [ ](https://seu-minio/.../audio.ogg)"
 *   Este script encontra esses links nas bolhas de mensagem e
 *   injeta um player nativo (<audio controls>/<video controls>)
 *   logo abaixo do texto — restaurando o botão de play.
 *
 * Compatibilidade:
 *   - Não depende de classes internas do Chatwoot (à prova de updates)
 *   - Funciona com mensagens novas em tempo real (MutationObserver)
 *   - Re-injeta se o Vue re-renderizar a conversa
 * ============================================================
 */
(function () {
  'use strict';

  var VERSION = '1.1.0';

  // Extensões reconhecidas (comparadas no pathname da URL)
  var AUDIO_RE = /\.(ogg|oga|opus|mp3|m4a|wav|aac)$/i;
  var VIDEO_RE = /\.(mp4|webm|mov|m4v|mkv|avi)$/i;
  // Fallback: URL crua dentro do texto (caso o sanitizador remova o <a>)
  var RAW_URL_RE = /https?:\/\/[^\s<>"')\]]+/gi;

  function mediaKind(url) {
    try {
      var p = decodeURIComponent(new URL(url, window.location.href).pathname);
      if (AUDIO_RE.test(p)) return 'audio';
      if (VIDEO_RE.test(p)) return 'video';
    } catch (e) { /* url inválida */ }
    return null;
  }

  function buildPlayer(kind, href) {
    var media;
    if (kind === 'video') {
      media = document.createElement('video');
      media.controls = true;
      media.preload = 'metadata';
      media.playsInline = true;
      media.style.cssText =
        'display:block;width:320px;max-width:100%;border-radius:10px;' +
        'background:#000;margin:6px 0 2px;';
    } else {
      media = document.createElement('audio');
      media.controls = true;
      media.preload = 'metadata';
      media.style.cssText =
        'display:block;width:280px;max-width:100%;height:44px;margin:6px 0 2px;';
    }
    media.src = href;

    media.addEventListener('error', function () {
      try {
        if (media.parentElement && !media.parentElement.querySelector('.eg-media-error')) {
          var err = document.createElement('div');
          err.className = 'eg-media-error';
          err.textContent = 'Não foi possível carregar a mídia';
          err.style.cssText = 'font-size:11px;opacity:.75;margin-top:-2px;';
          media.style.display = 'none';
          media.parentElement.appendChild(err);
        }
      } catch (e) { /* noop */ }
    });

    return media;
  }

  // Controle anti-duplicação:
  //  anchorPlayers: âncora -> player injetado (permite re-injeção se Vue remover)
  //  hostUrls: elemento hospedeiro -> { url: true } já processadas (fallback texto)
  var anchorPlayers = new WeakMap();
  var hostUrls = new WeakMap();

  function alreadyInjected(host, url) {
    var map = hostUrls.get(host);
    return !!(map && map[url]);
  }

  function markInjected(host, url) {
    if (!hostUrls.has(host)) hostUrls.set(host, {});
    hostUrls.get(host)[url] = true;
  }

  function injectIntoHost(host, kind, url) {
    if (!host || !host.parentElement) return;
    if (alreadyInjected(host, url)) return;

    var player = buildPlayer(kind, url);
    player.setAttribute('data-eg-url', url);
    host.insertAdjacentElement('afterend', player);
    markInjected(host, url);
  }

  // Caminho principal: âncoras <a> geradas pelo markdown [ ](url)
  function enhanceAnchors(root) {
    var anchors = (root || document).querySelectorAll('a[href]');
    for (var i = 0; i < anchors.length; i++) {
      (function (a) {
        var href = a.getAttribute('href');
        if (!href) return;
        var kind = mediaKind(href);
        if (!kind) return;

        var existing = anchorPlayers.get(a);
        if (existing && existing.isConnected) {
          markInjected(a.closest('.prose') || a.parentElement, href, existing);
          return;
        }

        var host = a.closest('.prose') || a.parentElement;
        if (!host || !host.parentElement) return;
        if (alreadyInjected(host, href)) {
          anchorPlayers.set(a, host.parentElement.querySelector('[data-eg-url]'));
          return;
        }

        var player = buildPlayer(kind, href);
        player.setAttribute('data-eg-url', href);
        host.insertAdjacentElement('afterend', player);
        anchorPlayers.set(a, player);
        markInjected(host, href);
      })(anchors[i]);
    }
  }

  // Extensões como substring (pré-filtro barato para nós de texto)
  var MEDIA_SUBSTR = ['.ogg', '.oga', '.opus', '.mp3', '.m4a', '.wav', '.aac', '.mp4', '.webm', '.mov', '.mkv'];

  // Fallback: URL crua em nós de texto (se o <a> não existir no DOM)
  function enhanceTextNodes() {
    var walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT, null, false);
    var nodes = [];
    while (walker.nextNode()) {
      var t = walker.currentNode.nodeValue || '';
      if (t.indexOf('http') === -1) continue;
      for (var k = 0; k < MEDIA_SUBSTR.length; k++) {
        if (t.indexOf(MEDIA_SUBSTR[k]) !== -1) {
          nodes.push(walker.currentNode);
          break;
        }
      }
    }
    for (var i = 0; i < nodes.length; i++) {
      var matches = nodes[i].nodeValue.match(RAW_URL_RE);
      if (!matches) continue;
      for (var j = 0; j < matches.length; j++) {
        var kind = mediaKind(matches[j]);
        if (!kind) continue;
        var parentEl = nodes[i].parentElement;
        var host = parentEl && (parentEl.closest('.prose') || parentEl);
        if (!host) continue;
        if (alreadyInjected(host, matches[j])) continue;
        injectIntoHost(host, kind, matches[j]);
      }
    }
  }

  var scheduled = null;
  function scheduleScan() {
    if (scheduled) return;
    scheduled = setTimeout(function () {
      scheduled = null;
      scan();
    }, 120);
  }

  function scan() {
    try {
      enhanceAnchors(document);
      enhanceTextNodes();
    } catch (e) {
      console.warn('[evolution-go] scan error:', e);
    }
  }

  function start() {
    scan();

    // Novas mensagens / re-render do Vue / troca de conversa (SPA)
    new MutationObserver(scheduleScan).observe(document.body, {
      childList: true,
      subtree: true,
    });

    // Rede de segurança periódica
    setInterval(scan, 2000);

    console.log('[evolution-go] Chatwoot Media Player v' + VERSION + ' ativo');
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start);
  } else {
    start();
  }
})();
