package whatsmeow_service

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"

	cwebp "github.com/chai2010/webp"
)

func imagemTeste(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x40, A: 0xff})
		}
	}
	return img
}

// webpAnimadoFalsifica monta um container WebP com chunk VP8X (o formato das
// figurinhas animadas do WhatsApp) sem imagem VP8/VP8L que o decodificador
// estatico consiga ler. E o formato real que faz a conversao para PNG falhar.
func webpAnimadoFalsifica() []byte {
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	// tamanho total do arquivo menos 8, preenchido abaixo
	sizePos := buf.Len()
	buf.Write([]byte{0, 0, 0, 0})
	buf.WriteString("WEBP")
	buf.WriteString("VP8X") // chunk estendido: suporta imagem unica, animacao e icones
	buf.Write([]byte{10, 0, 0, 0})
	// flags: 0b0000_0010 = imagem animada; alpha e perfil em 0
	buf.Write([]byte{0x02, 0x00, 0x00, 0x00})
	buf.Write([]byte{0x1f, 0x00, 0x00, 0x00}) // largura-1  = 31
	buf.Write([]byte{0x1f, 0x00, 0x00, 0x00}) // altura-1   = 31
	buf.Write([]byte{0x07, 0x00, 0x00, 0x00}) // frames-1   = 7
	riff := make([]byte, 4)
	binary.LittleEndian.PutUint32(riff, uint32(buf.Len()-8))
	copy(buf.Bytes()[sizePos:sizePos+4], riff)
	return buf.Bytes()
}

func TestConvertStickerToPNGConverteWebpEstatico(t *testing.T) {
	var webpBuf bytes.Buffer
	if err := cwebp.Encode(&webpBuf, imagemTeste(16, 16), &cwebp.Options{Lossless: true}); err != nil {
		t.Fatalf("falha ao gerar webp de teste: %v", err)
	}

	media, err := convertStickerToPNG(webpBuf.Bytes())
	if err != nil {
		t.Fatalf("webp estatico deveria converter, veio erro: %v", err)
	}
	if !media.converted {
		t.Fatal("webp estatico deveria ser reportado como convertido")
	}
	if media.extension != ".png" || media.mimeType != "image/png" {
		t.Fatalf("esperava .png/image/png, veio %s/%s", media.extension, media.mimeType)
	}
	if _, err := png.Decode(bytes.NewReader(media.data)); err != nil {
		t.Fatalf("bytes entregues nao sao PNG valido: %v", err)
	}
}

func TestConvertStickerToPNGWebpAnimadoNaoRotulaComoPng(t *testing.T) {
	// Regressao do bug: quando a conversao falha os bytes continuam WebP, mas
	// extensao e mimetype precisam dizer WebP. Antes o arquivo subia como .png
	// com conteudo WebP e o preview quebrava no Chatwoot.
	media, err := convertStickerToPNG(webpAnimadoFalsifica())
	if err == nil {
		t.Fatal("webp animado nao deveria converter para PNG sem erro")
	}
	if media.converted {
		t.Fatal("webp animado nao pode ser reportado como convertido")
	}
	if media.extension != ".webp" || media.mimeType != "image/webp" {
		t.Fatalf("esperava .webp/image/webp, veio %s/%s", media.extension, media.mimeType)
	}
	if !bytes.Equal(media.data, webpAnimadoFalsifica()) {
		t.Fatal("os bytes originais deveriam ter sido preservados")
	}
}

func TestConvertStickerToPNGJaEhPng(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, imagemTeste(8, 8)); err != nil {
		t.Fatalf("falha ao gerar png de teste: %v", err)
	}
	media, err := convertStickerToPNG(buf.Bytes())
	if err == nil {
		t.Fatal("bytes PNG nao deveriam ser lidos como webp sem erro")
	}
	if media.converted {
		t.Fatal("bytes PNG nao deveriam ser marcados como convertidos")
	}
	if media.extension != ".png" || media.mimeType != "image/png" {
		t.Fatalf("esperava .png/image/png, veio %s/%s", media.extension, media.mimeType)
	}
}
