// Package livecheck_test runs Promptdam against real providers, on keys
// the developer supplies; without them every test skips.
package livecheck_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/analytics"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/requestlog"
	"github.com/hasan-muzak/promptdam/server/features/routers"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
	"github.com/hasan-muzak/promptdam/server/features/routes"
)

// GümrükPilot is a customer's product: an assistant for Turkish customs
// brokers and accountants (belge kontrolü, GTİP, mevzuat, maliyet). It
// calls Promptdam with the Anthropic SDK's request shape and model
// "musavir", a router: Haiku 5.5 at low effort for the easy turns, Sonnet
// 5.5 for the rest, Haiku 5.5 deciding. One long conversation with a
// broker who is also a CPA, then the gateway's own ledger.
//
// Run: task demo:customs (PROMPTDAM_TEST_ANTHROPIC_KEY in .env, the stack
// up). PROMPTDAM_DEMO_TRANSCRIPT names a file for the whole conversation.
// It stops past maxSpend.
const maxSpend = 0.50 // dollars

const pilotSystem = `Sen GümrükPilot'sun: Türkiye'deki gümrük müşavirleri, gümrük müşavir yardımcıları ve serbest muhasebeci mali müşavirler (SMMM) için geliştirilmiş bir mevzuat ve operasyon asistanısın. Kullanıcıların mesleğini bilen uzmanlardır; onlara meslektaş gibi, açık ve kısa yanıt ver.

# Görevlerin
1. Belge analizi: ticari fatura, çeki listesi (packing list), konşimento, menşe belgesi gibi belgelerden ürünleri, miktarları, ağırlıkları, fiyatları ve menşe ülkelerini çıkar; belgeler arasındaki tutarsızlıkları bul.
2. GTİP / HS sınıflandırma: ürün tanımından, teknik özelliklerden veya katalogdan GTİP adayları öner, gerekçesini Armonize Sistem yorum kurallarına ve fasıl/pozisyon notlarına dayandır.
3. Mevzuat asistanı: 4458 sayılı Gümrük Kanunu, Gümrük Yönetmeliği, tebliğler ve genelgeler hakkındaki soruları kaynak göstererek yanıtla; eski ve yeni düzenlemeyi karşılaştır.
4. İthalat maliyet ve risk analizi: GTİP, menşe, gümrük kıymeti ve rejime göre gümrük vergisi, ilave gümrük vergisi (İGV), KDV ve diğer mali yükümlülükleri hesapla; gerekli izin ve denetimleri (TAREKS, uygunluk değerlendirmesi, ithalat lisansı vb.) belirt.
5. Şirketleşme ve vergi soruları: kullanıcılar çoğu zaman müşterilerinin şirket türünü, vergi yükünü ve sorumluluğunu da sorar. 6102 sayılı Türk Ticaret Kanunu, 193 sayılı Gelir Vergisi Kanunu, 5520 sayılı Kurumlar Vergisi Kanunu, 3065 sayılı KDV Kanunu ve 6183 sayılı Kanun çerçevesinde yanıtla.

# Yanıt kuralları
- Türkçe yanıt ver. Kullanıcı başka dilde metin isterse (ör. tedarikçiye İngilizce e-posta) o dilde yaz.
- Kısa başla: önce doğrudan cevap, sonra gerekçe. Kullanıcı ayrıntı istemedikçe 250 kelimeyi geçme; karşılaştırma ve hesaplamalarda tablo kullan.
- Kaynak göster: kanun adı, sayısı ve maddesi. Madde numarasından emin değilsen numara uydurma; "ilgili madde kontrol edilmeli" de.
- Kesin bilgi ile kontrol edilmesi gereken bilgiyi ayır. Oranlar, tutarlar, eşikler ve dilimler sık değişir: güncel Resmî Gazete, Türk Gümrük Tarife Cetveli (TGTC), İthalat Rejimi Kararı ve ilgili Cumhurbaşkanı Kararları ile kontrol edilmesini hatırlat.
- Araç sonuçları senin bilgi kaynağındır. Araç sonucu "DEMO VERİSİ" diyorsa bunu yanıtında açıkça belirt.
- Hukuki ve vergisel sonuçları olan konularda son kararın kullanıcıya ve müşterisine ait olduğunu, gerekirse yazılı görüş alınmasını belirt; ama her yanıta uzun uyarı ekleme, bir cümle yeter.
- Uzun tire kullanma; kısa çizgi, virgül veya iki nokta kullan.
- Kullanıcının verdiği kur, tutar ve oranları kullan; varsayım yapman gerekirse varsayımı yaz.

# Hesaplama kuralları
- Gümrük kıymeti, ithal eşyasının Türkiye gümrük bölgesine giriş yerine kadar olan değeridir: FOB teslimde navlun ve sigorta eklenir (CIF esası).
- Döviz çevriminde beyannamenin tescil tarihindeki TCMB döviz alış kuru esas alınır; kullanıcı kur verdiyse onu kullan ve bunu belirt.
- Gümrük vergisi = gümrük kıymeti x gümrük vergisi oranı. İGV = gümrük kıymeti x İGV oranı (İGV, İthalat Rejimi Kararı eki listelere göre menşe bazında uygulanır).
- İthalde KDV matrahı (3065 sayılı Kanun md. 21): gümrük vergisine esas değer + ithalat sırasında ödenen vergi, resim, harç ve benzeri (gümrük vergisi, İGV, varsa ÖTV) + mal teslim edilinceye kadar yapılan ve faturada yer almayan diğer giderler. Genel KDV oranı %20'dir; malın oranını kontrol et.
- Her hesabı adım adım göster: kalem, formül, tutar. Sonda toplam vergi yükünü ve gümrük kıymetine oranını ver. Yuvarlamayı kuruş düzeyinde yap.

# Belge kontrolü kuralları
Faturayı, çeki listesini ve diğer belgeleri şu alanlarda karşılaştır: belge numarası ve tarihi, satıcı ve alıcı, mal tanımı ve model, miktar ve birim, kap adedi ve cinsi, net ve brüt ağırlık, birim fiyat ve toplam, teslim şekli (Incoterms), menşe ülke, GTİP. Her tutarsızlık için: alan, belge A'daki değer, belge B'deki değer, risk (yüksek/orta/düşük) ve önerilen işlem. Menşe, miktar ve kıymet farklarını yüksek risk say: yanlış beyan, eksik vergi, menşe ispatı ve ceza riski doğurur.

# Şirket türleri: başvuru notları (6102 sayılı TTK ve vergi kanunları)
- Şahıs işletmesi: tüzel kişiliği yoktur; işletme sahibi tüm borçlardan şahsen ve sınırsız sorumludur. Kazanç, Gelir Vergisi Kanunu'na göre artan oranlı tarifeyle (dilimler her yıl güncellenir) vergilendirilir. Kuruluşu hızlı ve ucuzdur.
- Kollektif şirket: gerçek kişi ortaklar şirket borçlarından sınırsız ve müteselsil sorumludur. Az kullanılır.
- Komandit şirket: en az bir ortağın sorumluluğu sınırsız (komandite), diğerlerinin sınırlıdır (komanditer). Az kullanılır.
- Limited şirket: 1 ile 50 arasında ortak. Asgari esas sermaye 50.000 TL (1.1.2024'ten itibaren, Cumhurbaşkanı Kararı). Ortaklar şirkete karşı sermaye taahhüdüyle sorumludur; ancak şirketten tahsil edilemeyen amme alacaklarından sermaye payları oranında sorumlu tutulabilirler (6183 sayılı Kanun md. 35). Kurumlar vergisine tabidir.
- Anonim şirket: tek pay sahibiyle kurulabilir. Asgari esas sermaye 250.000 TL, kayıtlı sermaye sisteminde başlangıç sermayesi 500.000 TL (1.1.2024'ten itibaren). Pay sahibi kural olarak yalnız şirkete karşı ve taahhüt ettiği sermaye ile sorumludur; kanuni temsilcilerin amme alacağı sorumluluğu 6183 sayılı Kanun mükerrer md. 35'e göre doğabilir. Kurumlar vergisine tabidir. Pay devri limited şirkete göre kolaydır.
- Kooperatif: ortaklarının ekonomik menfaatlerini karşılıklı yardımla sağlamayı amaçlar; ticari ithalat için nadiren uygundur.
- Kurumlar vergisi genel oranı %25'tir; ihracat ve üretim kazançları için indirimli oranlar olabilir, güncel durumu kontrol et. Dağıtılan kâr ayrıca stopaja tabidir.
- Şirket türü seçiminde şunları birlikte değerlendir: sorumluluk, kazanç düzeyine göre efektif vergi yükü, kâr dağıtımı stopajı, kuruluş ve muhasebe maliyetleri, sermaye ihtiyacı, ortak sayısı ve pay devri, banka ve tedarikçi nezdinde güvenilirlik, ileride Yetkilendirilmiş Yükümlü Sertifikası (YYS) gibi statülere başvuru.

# Gümrük müşavirliği notları (4458 sayılı Gümrük Kanunu)
- Temsil: herkes gümrük idaresi nezdindeki işlemleri için bir temsilci atayabilir; temsil doğrudan (başkası adına ve hesabına) veya dolaylı (kendi adına, başkası hesabına) olabilir (4458 sayılı Kanun md. 5).
- Gümrük müşavirleri ve yardımcıları, Kanun'un gümrük müşavirliğine ilişkin hükümleri (md. 227 ve devamı) ile Gümrük Yönetmeliği çerçevesinde çalışır; beyanın doğruluğu ve belgelerin saklanması konusundaki yükümlülüklerini hatırlat.
- Yükümlülük ve ceza konularında (ör. md. 234 ve 235 kapsamındaki vergi farkı ve para cezaları) somut olayın koşullarına göre değerlendirme yapılması gerektiğini belirt.

# Çıktı biçimleri
- Karşılaştırma: Markdown tablosu, sonra "Öneri" başlığıyla 2-3 madde.
- Hesaplama: "Varsayımlar", "Hesap" (tablo), "Sonuç" başlıkları.
- Belge kontrolü: tablo (Alan | Fatura | Çeki listesi | Risk | Öneri), sonra "İlk yapılacaklar".
- E-posta: konu satırı ve gövde; kibar, kısa, maddeli.
- Özet ve yapılacaklar: numaralı liste, her maddede sorumlu ve öncelik.`

// The tools GümrükPilot gives the model; their answers are demo data.
const pilotTools = `[
 {"name":"gtip_ara","description":"Türk Gümrük Tarife Cetveli'nde ürün tanımına göre GTİP adaylarını arar. Sınıflandırma sorularında önce bunu kullan.",
  "input_schema":{"type":"object","properties":{"sorgu":{"type":"string","description":"Ürün tanımı ve ayırt edici özellikleri"}},"required":["sorgu"]}},
 {"name":"tarife_sorgula","description":"Bir GTİP ve menşe ülke için gümrük vergisi, İGV, KDV oranlarını ve ithalat denetimlerini getirir.",
  "input_schema":{"type":"object","properties":{"gtip":{"type":"string"},"mense":{"type":"string","description":"Menşe ülke"}},"required":["gtip","mense"]}}
]`

func runTool(name string, input json.RawMessage) string {
	switch name {
	case "gtip_ara":
		return `DEMO VERİSİ (gerçek tarife sorgusu değildir). Adaylar:
1. 8422.19.00.00.00 Bulaşık yıkama makineleri: diğerleri (ev tipi olmayanlar). Endüstriyel, tünel ve kapaklı tipler buraya girer.
2. 8422.11.00.00.00 Bulaşık yıkama makineleri: ev tipi olanlar.
3. 8419.89.98.00.19 Isı değişimi yoluyla madde işleyen diğer makineler (kurutma ünitesi ayrı gelirse değerlendirilir).`
	case "tarife_sorgula":
		var in struct{ GTIP, Mense string }
		_ = json.Unmarshal(input, &in)
		return fmt.Sprintf(`DEMO VERİSİ (gerçek tarife sorgusu değildir). GTİP %s, menşe %s:
- Gümrük vergisi: %%2,2
- İlave gümrük vergisi (İGV): %%20 (menşe Çin için, demo)
- KDV: %%20
- ÖTV: yok
- Denetim: TAREKS kaydı, makine emniyeti (CE uygunluk beyanı ve teknik dosya) istenir.
- Not: Menşe Vietnam olsaydı İGV oranı farklı olabilirdi; menşe ispatı belgesi gerekir.`, in.GTIP, in.Mense)
	}
	return "Bilinmeyen araç."
}

// The broker's side of the conversation: a CPA who is also a customs
// broker, helping a client start importing.
var brokerTurns = []string{
	"Merhaba. Ben İzmir'de SMMM ve gümrük müşaviriyim. Bir müşterim endüstriyel mutfak ekipmanı ithalatına başlayacak. Türkiye'deki şirket türlerini kısaca listeler misin?",
	"Müşterim tek kişi, ilk yıl yaklaşık 15 milyon TL ithalat ve 1,5 milyon TL civarında vergi öncesi kâr bekliyor. Şahıs işletmesi, limited şirket ve anonim şirketi; sorumluluk, asgari sermaye, vergi yükü ve ileride YYS başvurusu açısından karşılaştırır mısın? Önerin ne olur?",
	"Limited şirkette ortak sayısı en fazla kaç olabiliyordu?",
	"Müşterinin ilk ithalatı Çin menşeli, AISI 304 paslanmaz çelik, saatte 60 sepet kapasiteli tünel tipi endüstriyel bulaşık makinesi. GTİP önerir misin? Tarife veritabanına bak, sonra Çin menşei için oranları da getir.",
	"Fatura 4 set için toplam 18.400 USD, FOB Şanghay. Navlun 2.150 USD, sigorta 160 USD. Kuru 1 USD = 41,20 TL varsayalım. Gümrük vergisi, İGV ve KDV'yi adım adım hesaplar mısın?",
	`Tedarikçiden belgeler geldi. Karşılaştırır mısın?

COMMERCIAL INVOICE No: SH-26-0917  Date: 2026-09-17
Seller: Shanghai Hengli Kitchen Equipment Co., Ltd.
Buyer: Ege Mutfak Ekipmanlari Ltd. Sti., Izmir, Turkiye
Terms: FOB Shanghai
Description: Tunnel type commercial dishwasher, model HL-T60, 60 racks/hour, stainless steel AISI 304
Quantity: 4 sets   Unit price: USD 4,600.00   Total: USD 18,400.00
Country of origin: China   HS code: 8422.19

PACKING LIST No: SH-26-0917  Date: 2026-09-18
Description: Tunnel type commercial dishwasher, model HL-T60S
Quantity: 3 sets in 4 wooden cases
Net weight: 1,240 kg   Gross weight: 1,480 kg
Country of origin: Vietnam
Marks: EGE-IZM-01/04`,
	"Teşekkürler. Bu tutarsızlıklar için tedarikçiye gönderilecek kısa bir İngilizce e-posta taslağı yazar mısın?",
	"Bu ithalatı müşterim adına ben yapacağım. 4458 sayılı Kanun'a göre doğrudan temsil ile dolaylı temsil arasındaki fark nedir, gümrük müşaviri olarak sorumluluğum hangisinde nasıl değişir?",
	"Bunu müşterime anlatacağım; üç maddelik sade bir özet yapar mısın?",
	"Menşe Vietnam çıkarsa hesap nasıl değişir? Sadece değişen kalemleri söyle.",
	"Son olarak bugün konuştuklarımızdan bir yapılacaklar listesi çıkarır mısın?",
}

type pilotUsage struct {
	Input      uint64 `json:"input_tokens"`
	CacheWrite uint64 `json:"cache_creation_input_tokens"`
	CacheRead  uint64 `json:"cache_read_input_tokens"`
	Output     uint64 `json:"output_tokens"`
}

type pilotAnswer struct {
	Model      string            `json:"model"`
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      pilotUsage        `json:"usage"`
}

func (a pilotAnswer) text() string {
	var b strings.Builder
	for _, c := range a.Content {
		var blk struct{ Type, Text string }
		_ = json.Unmarshal(c, &blk)
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

func TestCustomsBroker(t *testing.T) {
	key := os.Getenv("PROMPTDAM_TEST_ANTHROPIC_KEY")
	if key == "" {
		t.Skip("no PROMPTDAM_TEST_ANTHROPIC_KEY")
	}
	ctx := context.Background()
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
	w.Grant(catalog.Routers, catalog.RoutingLearned, catalog.PromptCaching)
	// The prices production syncs daily.
	if err := w.App.Prices.Sync(ctx, true); err != nil {
		t.Fatalf("syncing prices: %v", err)
	}
	list := w.App.Prices.Current()
	for _, m := range []string{"claude-haiku-5-5", "claude-sonnet-5-5"} {
		p, ok := list.Lookup("anthropic", m)
		if !ok {
			t.Fatalf("no price for %s", m)
		}
		t.Logf("price %s: input $%g, cache write $%g, cache read $%g, output $%g per million", m, p.Input, p.CacheWrite, p.CachedInput, p.Output)
	}

	if _, err := w.App.Providers.Create(w.Scoped(), w.Org, w.Project, providers.Input{Name: "anthropic", Kind: "anthropic",
		APIKey: &key, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.App.Routers.Create(w.Scoped(), w.Org, w.Project, routers.Input{Name: "musavir", Status: tiers.Active,
		DecisionModel: "anthropic/claude-haiku-5-5", DecisionTimeoutMS: 2000,
		Spec: tiers.Spec{Tiers: []tiers.Tier{
			{Name: "fast", Model: "anthropic/claude-haiku-5-5", Effort: "low",
				Description: "Definitions and single facts, rewording, translations, short emails, summaries and to-do lists of what the conversation already covered.",
				Examples:    []string{"Limited şirkette ortak sayısı en fazla kaç olabilir?", "Bunu üç maddede özetler misin?"}},
			{Name: "expert", Model: "anthropic/claude-sonnet-5-5", Effort: "medium",
				Description: "Legal analysis and comparisons across laws, tax and liability trade-offs, customs value and tax calculations, tariff classification with reasoning, checking documents against each other.",
				Examples:    []string{"Şahıs şirketi ile limited şirketi vergi ve sorumluluk açısından karşılaştır."}},
		}, Default: "expert"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: "default", PromptCache: true}); err != nil {
		t.Fatal(err)
	}

	system, _ := json.Marshal(pilotSystem)
	var history []json.RawMessage
	var spent float64
	var transcript strings.Builder
	transcript.WriteString("# GümrükPilot through Promptdam: the conversation\n\n")
	start := time.Now()

	call := func() (pilotAnswer, string) {
		t.Helper()
		msgs, _ := json.Marshal(history)
		body := `{"model":"musavir","max_tokens":4000,"system":` + string(system) + `,"tools":` + pilotTools + `,"messages":` + string(msgs) + `}`
		status, h, out := w.Call("/v1/messages", body, brand.HeaderSession, "pilot-conv-42", brand.HeaderUser, "smmm-izmir")
		if status != 200 {
			t.Fatalf("status %d: %s", status, out)
		}
		var a pilotAnswer
		if err := json.Unmarshal([]byte(out), &a); err != nil {
			t.Fatal(err)
		}
		served := h.Get(brand.HeaderServedModel)
		if p, ok := list.Lookup("anthropic", served); ok {
			spent += float64(requestlog.CostAt(p, wire.Usage{Usage: wireUsage(a.Usage)})) / 1e6
		}
		assistant, _ := json.Marshal(map[string]any{"role": "assistant", "content": a.Content})
		history = append(history, assistant)
		tier := h.Get(brand.HeaderTier) + " (" + h.Get(brand.HeaderTierSource) + "), " + served
		if e := h.Get(brand.HeaderEffort); e != "" {
			tier += ", effort " + e
		}
		t.Logf("   %s: in %d, cache write %d, read %d, out %d, stop %s", tier, a.Usage.Input, a.Usage.CacheWrite, a.Usage.CacheRead,
			a.Usage.Output, a.StopReason)
		if spent > maxSpend {
			t.Fatalf("spent $%.3f, over the demo's $%.2f", spent, maxSpend)
		}
		return a, tier
	}

	for i, ask := range brokerTurns {
		t.Logf("turn %d: %s", i+1, clip(ask, 80))
		user, _ := json.Marshal(map[string]any{"role": "user", "content": ask})
		history = append(history, user)
		fmt.Fprintf(&transcript, "## %d. Broker\n\n%s\n\n", i+1, ask)
		a, tier := call()
		for loop := 0; a.StopReason == "tool_use" && loop < 4; loop++ {
			var results []map[string]any
			for _, c := range a.Content {
				var blk struct {
					Type, ID, Name string
					Input          json.RawMessage
				}
				_ = json.Unmarshal(c, &blk)
				if blk.Type == "tool_use" {
					out := runTool(blk.Name, blk.Input)
					fmt.Fprintf(&transcript, "*Tool %s(%s) → %s*\n\n", blk.Name, blk.Input, clip(out, 120))
					results = append(results, map[string]any{"type": "tool_result", "tool_use_id": blk.ID, "content": out})
				}
			}
			user, _ := json.Marshal(map[string]any{"role": "user", "content": results})
			history = append(history, user)
			a, tier = call()
		}
		fmt.Fprintf(&transcript, "**GümrükPilot** (%s)\n\n%s\n\n", tier, a.text())
	}
	t.Logf("the conversation took %s; the app's own count of spend: $%.4f", time.Since(start).Round(time.Second), spent)

	// The gateway's own ledger, as the request log stored it.
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		TurnKind       string `ch:"turn_kind"`
		HumanTurns     uint16 `ch:"human_turns"`
		Tier           string `ch:"tier"`
		Source         string `ch:"tier_source"`
		Decided        string `ch:"decided_tier"`
		Served         string `ch:"served_model"`
		Effort         string `ch:"effort"`
		Input          uint32 `ch:"input_tokens"`
		CacheRead      uint32 `ch:"cache_read_tokens"`
		CacheWrite     uint32 `ch:"cache_write_tokens"`
		Output         uint32 `ch:"output_tokens"`
		Cost           int64  `ch:"cost_micros"`
		Counterfactual int64  `ch:"counterfactual_micros"`
		Savings        int64  `ch:"savings_micros"`
		Reason         string `ch:"savings_reason"`
		Basis          string `ch:"savings_basis"`
		Router         int64  `ch:"router_micros"`
		Latency        uint32 `ch:"latency_ms"`
	}
	if err := w.C.Events.Select(ctx, &rows, events.Project(w.Project), `SELECT turn_kind, human_turns, tier, tier_source, decided_tier,
		served_model, effort, input_tokens, cache_read_tokens, cache_write_tokens, output_tokens, cost_micros, counterfactual_micros,
		savings_micros, savings_reason, savings_basis, router_micros, latency_ms FROM requests WHERE {tenant} ORDER BY ts`); err != nil {
		t.Fatal(err)
	}
	var ledger strings.Builder
	ledger.WriteString("| # | Turn | Kind | Tier (source) | Served | Effort | In | Cache read | Cache write | Out | Cost | Without Promptdam | Saved | Reason (basis) | Router | ms |\n")
	ledger.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	var cost, cf, saved, router int64
	for i, r := range rows {
		fmt.Fprintf(&ledger, "| %d | %d | %s | %s (%s) | %s | %s | %d | %d | %d | %d | $%.5f | $%.5f | $%.5f | %s (%s) | $%.5f | %d |\n",
			i+1, r.HumanTurns, r.TurnKind, r.Tier, r.Source, strings.TrimPrefix(r.Served, "claude-"), r.Effort, r.Input, r.CacheRead,
			r.CacheWrite, r.Output, usd(r.Cost), usd(r.Counterfactual), usd(r.Savings), r.Reason, r.Basis, usd(r.Router), r.Latency)
		cost, cf, saved, router = cost+r.Cost, cf+r.Counterfactual, saved+r.Savings, router+r.Router
	}
	fmt.Fprintf(&ledger, "\nCalls %d. Cost $%.4f (+ router $%.4f). Without Promptdam $%.4f. Saved $%.4f (%.0f%%).\n",
		len(rows), usd(cost), usd(router), usd(cf), usd(saved), 100*float64(saved)/float64(max(cf, 1)))
	report, err := analytics.New(w.C.Events).Savings(ctx, w.Project, start.Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&ledger, "\nThe savings report (what the dashboard reads): cost $%.4f, without Promptdam $%.4f, saved $%.4f.\n",
		usd(report.Total.Cost), usd(report.Total.Counterfactual), usd(report.Total.Savings))
	for _, r := range report.ByReason {
		fmt.Fprintf(&ledger, "- reason %q: %d calls, saved $%.4f\n", r.Key, r.Requests, usd(r.Savings))
	}
	for _, r := range report.ByBasis {
		fmt.Fprintf(&ledger, "- basis %q: %d calls, saved $%.4f\n", r.Key, r.Requests, usd(r.Savings))
	}
	t.Log("\n" + ledger.String())
	if path := os.Getenv("PROMPTDAM_DEMO_TRANSCRIPT"); path != "" {
		transcript.WriteString("# The gateway's ledger\n\n" + ledger.String())
		if err := os.WriteFile(path, []byte(transcript.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// wireUsage is an Anthropic answer's usage as the gateway counts it: input
// with the cached tokens in it.
func wireUsage(u pilotUsage) llm.Usage {
	return llm.Usage{Input: u.Input + u.CacheRead + u.CacheWrite, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Output: u.Output}
}

func usd(micros int64) float64 { return float64(micros) / 1e6 }

func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
