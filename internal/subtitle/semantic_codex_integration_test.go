package subtitle

import (
	"context"
	"math"
	"os"
	"testing"
	"time"
)

// This opt-in test spends one real Codex subscription request. It is excluded
// from ordinary CI and is run manually before deploying changes to the
// exceptional subtitle-only production path.
func TestCodexSemanticIntegrationHindiSDH25ToEnglish23976(t *testing.T) {
	if os.Getenv("REDSYNC_TEST_CODEX") != "1" {
		t.Skip("set REDSYNC_TEST_CODEX=1 for the real subscription-authenticated integration test")
	}
	pairs := [][2]string{
		{"Meet me at the old railway station after midnight.", "आधी रात के बाद पुराने रेलवे स्टेशन पर मुझसे मिलना।"},
		{"Doctor Mehra said the antidote expires in forty minutes.", "डॉक्टर मेहरा ने कहा कि मारक दवा चालीस मिनट में बेअसर हो जाएगी।"},
		{"The blue suitcase belongs to passenger number seventeen.", "नीला सूटकेस यात्री संख्या सत्रह का है।"},
		{"Do not open the northern gate until Captain Rao arrives.", "कैप्टन राव के आने तक उत्तरी फाटक मत खोलना।"},
		{"We found three fingerprints on the broken telescope.", "टूटी हुई दूरबीन पर हमें तीन उंगलियों के निशान मिले।"},
		{"Her flight leaves Delhi at six fifteen tomorrow morning.", "उसकी उड़ान कल सुबह छह बजकर पंद्रह मिनट पर दिल्ली से निकलेगी।"},
		{"The witness hid the photograph beneath the library stairs.", "गवाह ने तस्वीर पुस्तकालय की सीढ़ियों के नीचे छिपाई थी।"},
		{"Turn off the generator before you cross the flooded tunnel.", "पानी भरी सुरंग पार करने से पहले जनरेटर बंद कर देना।"},
		{"Only Professor Sen knows the password to laboratory nine.", "प्रयोगशाला नौ का पासवर्ड केवल प्रोफेसर सेन जानते हैं।"},
		{"The second map marks a village beyond the eastern ridge.", "दूसरे नक्शे में पूर्वी पहाड़ी के पार एक गांव दिखाया गया है।"},
		{"Call Inspector Verma and secure every entrance immediately.", "इंस्पेक्टर वर्मा को बुलाओ और तुरंत सभी प्रवेश द्वार सुरक्षित करो।"},
		{"I left the silver key inside your green notebook.", "मैंने चांदी की चाबी तुम्हारी हरी नोटबुक के अंदर रखी है।"},
		{"The storm will reach the harbor before sunrise.", "तूफान सूर्योदय से पहले बंदरगाह पहुंच जाएगा।"},
		{"Nobody knew the painting contained a hidden message.", "किसी को पता नहीं था कि उस चित्र में एक गुप्त संदेश था।"},
		{"Take platform four and wait beside the empty ticket office.", "प्लेटफॉर्म चार पर जाकर खाली टिकट कार्यालय के पास इंतजार करो।"},
		{"Our radio signal disappeared near the abandoned observatory.", "परित्यक्त वेधशाला के पास हमारा रेडियो संकेत गायब हो गया।"},
		{"The final envelope must reach Judge Kapoor before noon.", "आखिरी लिफाफा दोपहर से पहले जज कपूर तक पहुंचना चाहिए।"},
		{"She counted seven lanterns along the river bank.", "उसने नदी के किनारे सात लालटेनें गिनीं।"},
		{"This recording proves the meeting happened on Tuesday.", "यह रिकॉर्डिंग साबित करती है कि बैठक मंगलवार को हुई थी।"},
		{"Leave the medicine beside the child's wooden bed.", "दवा बच्चे के लकड़ी वाले बिस्तर के पास रख दो।"},
		{"The rescue boat cannot sail without a working compass.", "चालू कंपास के बिना बचाव नाव नहीं चल सकती।"},
		{"Commander Iqbal moved the convoy to sector twelve.", "कमांडर इकबाल ने काफिले को सेक्टर बारह में भेज दिया।"},
		{"We promised to return the necklace to its rightful owner.", "हमने हार उसके असली मालिक को लौटाने का वादा किया था।"},
		{"At dawn, ring the temple bell exactly twice.", "भोर में मंदिर की घंटी ठीक दो बार बजाना।"},
	}

	const offset = -4.25
	scale := 25.0 / (24000.0 / 1001.0)
	reference := make([]Cue, 0, len(pairs))
	target := make([]Cue, 0, len(pairs)+4)
	for i, dialogue := range pairs {
		refStart := 20.0 + float64(i)*24.0
		refEnd := refStart + 2.4
		targetStart := (refStart - offset) / scale
		targetEnd := (refEnd - offset) / scale
		if i%6 == 2 {
			target = append(target, cueSeconds(targetStart-1.2, targetStart-.5, "[तेज संगीत बजता है]"))
		}
		reference = append(reference, cueSeconds(refStart, refEnd, dialogue[0]))
		target = append(target, cueSeconds(targetStart, targetEnd, dialogue[1]))
	}

	model := os.Getenv("REDSYNC_TEST_CODEX_MODEL")
	if model == "" {
		model = "gpt-5.4-mini"
	}
	matcher := &CodexAnchorMatcher{
		Binary: "/home/nuclearnadal/.local/bin/codex", Model: model,
		ReasoningEffort: "low", Timeout: 45 * time.Second, MaxAnchors: 20,
	}
	actualRunner := &CodexAnchorMatcher{
		Binary: matcher.Binary, Model: matcher.Model,
		ReasoningEffort: matcher.ReasoningEffort, Timeout: matcher.Timeout,
	}
	matcher.Run = func(ctx context.Context, prompt string, schema []byte) ([]byte, error) {
		raw, err := actualRunner.runCodex(ctx, prompt, schema)
		t.Logf("Codex sparse-anchor result: %s", raw)
		return raw, err
	}
	alignment, err := AlignCodexSemantic(context.Background(), reference, target, matcher, SemanticOptions{
		AlignOptions: AlignOptions{MaxOffsetSeconds: 60, MinGapSeconds: .35, MaxSegments: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	if alignment.Samples < 10 {
		t.Fatalf("only %d semantic anchors survived", alignment.Samples)
	}
	if math.Abs(alignment.Scale-scale) > 0.0002 {
		t.Fatalf("scale = %.9f, want %.9f", alignment.Scale, scale)
	}
	if math.Abs(float64(alignment.OffsetMS)-offset*1000) > 150 {
		t.Fatalf("offset = %dms, want %.0fms", alignment.OffsetMS, offset*1000)
	}
	if alignment.ResidualMS > 100 {
		t.Fatalf("residual = %dms", alignment.ResidualMS)
	}
}
