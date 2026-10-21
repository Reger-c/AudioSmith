package gallery

import vd "github.com/hehehai/AudioSmith/internal/voicedesign"

// The earlier gallery's tables: its categories, timbre flavors and
// locales.

// legacyCategories are the earlier gallery's use-case categories.
var legacyCategories = []Category{
	{"narration", tx("Narration & Audiobooks", "旁白与有声书"), tx("Long-form reading: novels, stories, poems.", "长篇朗读：小说、故事、诗歌。")},
	{"conversational", tx("Conversational & Podcast", "对话与播客"), tx("Hosts, chats, assistants and support lines.", "主持、闲聊、助手与客服。")},
	{"characters", tx("Characters & Animation", "角色与动画"), tx("Heroes, villains, sidekicks and cartoon voices.", "英雄、反派、跟班与卡通角色。")},
	{"social", tx("Social Media & Short Video", "社交媒体与短视频"), tx("Vlogs, reviews, hype edits and explainers.", "Vlog、测评、高燃剪辑与快速讲解。")},
	{"advertising", tx("Ads & Promo", "广告与宣传"), tx("Commercials, trailers, jingles and brand voices.", "商业广告、预告片、电台广告与品牌声音。")},
	{"education", tx("Education & Explainers", "教育与讲解"), tx("Lectures, tutorials, guides and language practice.", "课程、教程、导览与语言练习。")},
	{"news", tx("News & Documentary", "新闻与纪录片"), tx("Anchors, reporters, commentators and documentaries.", "主播、记者、解说与纪录片。")},
	{"games", tx("Games", "游戏"), tx("NPCs, announcers, bosses and companions.", "NPC、播报员、Boss 与同伴。")},
	{"wellness", tx("Meditation & ASMR", "冥想与助眠"), tx("Guided calm, sleep stories and soft whispers.", "引导放松、助眠故事与轻声耳语。")},
}

func legacyCategoryIndex(id string) int {
	for i, c := range legacyCategories {
		if c.ID == id {
			return i
		}
	}
	return -1
}

// flavor is the timbre variation of a generated archetype: a name prefix
// plus attribute changes.
type flavor struct {
	name   vd.Text // "Warm" / "温暖的"
	pitch  string
	styles []string
}

var flavors = map[string]flavor{
	"warm":     {tx("Warm", "温暖的"), "", []string{"warm"}},
	"deep":     {tx("Deep", "低沉的"), "low", nil},
	"bright":   {tx("Bright", "明亮的"), "", []string{"bright"}},
	"crisp":    {tx("Crisp", "清晰的"), "", []string{"crisp"}},
	"gravelly": {tx("Gravelly", "沙哑的"), "low", []string{"raspy"}},
	"soft":     {tx("Gentle", "轻柔的"), "", []string{"soft"}},
	"smooth":   {tx("Smooth", "圆润的"), "", []string{"smooth"}},
	"velvet":   {tx("Velvet", "磁性的"), "low", []string{"magnetic"}},
	"lively":   {tx("Lively", "活力的"), "", []string{"energetic"}},
	"calm":     {tx("Calm", "沉稳的"), "", []string{"calm"}},
	"bold":     {tx("Bold", "有力的"), "", []string{"authoritative"}},
	"friendly": {tx("Friendly", "亲切的"), "", []string{"friendly"}},
	"playful":  {tx("Playful", "俏皮的"), "", []string{"playful"}},
	"sweet":    {tx("Sweet", "甜美的"), "high", []string{"sweet"}},
	"airy":     {tx("Airy", "气声的"), "", []string{"breathy"}},
	"whisper":  {tx("Whispering", "耳语的"), "", []string{"whisper", "soft"}},
	"resonant": {tx("Resonant", "浑厚的"), "low", []string{"resonant"}},
	"nasal":    {tx("Nasal", "鼻音的"), "", []string{"nasal"}},
	"dramatic": {tx("Dramatic", "戏剧化的"), "", []string{"dramatic"}},
	"polished": {tx("Polished", "专业的"), "", []string{"professional"}},
	"breathy":  {tx("Breathy", "带气声的"), "", []string{"breathy", "soft"}},
}

// locale is where a generated archetype's voice comes from: an English
// accent, a Chinese dialect, or a native language other than English.
type locale struct {
	name    vd.Text // "British" / "英式"
	accent  string
	dialect string
	langs   []string // recommended languages; the first is the preview language
	sample  string   // preview line for languages other than en/zh
}

var locales = map[string]locale{
	"en-us":    {name: tx("American", "美式"), accent: "american", langs: []string{"en"}},
	"en-gb":    {name: tx("British", "英式"), accent: "british", langs: []string{"en"}},
	"en-au":    {name: tx("Australian", "澳洲"), accent: "australian", langs: []string{"en"}},
	"en-ca":    {name: tx("Canadian", "加拿大"), accent: "canadian", langs: []string{"en"}},
	"en-in":    {name: tx("Indian English", "印度英语"), accent: "indian", langs: []string{"en", "hi"}},
	"en-ie":    {name: tx("Irish", "爱尔兰"), accent: "irish", langs: []string{"en"}},
	"en-sc":    {name: tx("Scottish", "苏格兰"), accent: "scottish", langs: []string{"en"}},
	"en-nz":    {name: tx("New Zealand", "新西兰"), accent: "new_zealand", langs: []string{"en"}},
	"en-za":    {name: tx("South African", "南非"), accent: "south_african", langs: []string{"en"}},
	"en-south": {name: tx("Southern US", "美国南方"), accent: "southern_us", langs: []string{"en"}},
	"en-cn":    {name: tx("Chinese-accented", "中式英语"), accent: "chinese", langs: []string{"en", "zh"}},
	"en-kr":    {name: tx("Korean-accented", "韩式英语"), accent: "korean", langs: []string{"en", "ko"}},
	"en-jp":    {name: tx("Japanese-accented", "日式英语"), accent: "japanese", langs: []string{"en", "ja"}},
	"en-br":    {name: tx("Brazilian-accented", "巴西口音英语"), accent: "portuguese", langs: []string{"en", "pt"}},
	"en-ru":    {name: tx("Russian-accented", "俄式英语"), accent: "russian", langs: []string{"en", "ru"}},
	"en-fr":    {name: tx("French-accented", "法式英语"), accent: "french", langs: []string{"en", "fr"}},
	"en-de":    {name: tx("German-accented", "德式英语"), accent: "german", langs: []string{"en", "de"}},
	"en-es":    {name: tx("Spanish-accented", "西语口音英语"), accent: "spanish", langs: []string{"en", "es"}},

	"zh":              {name: tx("Mandarin", "普通话"), langs: []string{"zh"}},
	"zh-sichuan":      {name: tx("Sichuan", "四川话"), dialect: "sichuan", langs: []string{"zh"}},
	"zh-dongbei":      {name: tx("Northeastern", "东北话"), dialect: "northeastern", langs: []string{"zh"}},
	"zh-henan":        {name: tx("Henan", "河南话"), dialect: "henan", langs: []string{"zh"}},
	"zh-shaanxi":      {name: tx("Shaanxi", "陕西话"), dialect: "shaanxi", langs: []string{"zh"}},
	"zh-guizhou":      {name: tx("Guizhou", "贵州话"), dialect: "guizhou", langs: []string{"zh"}},
	"zh-yunnan":       {name: tx("Yunnan", "云南话"), dialect: "yunnan", langs: []string{"zh"}},
	"zh-guilin":       {name: tx("Guilin", "桂林话"), dialect: "guilin", langs: []string{"zh"}},
	"zh-jinan":        {name: tx("Jinan", "济南话"), dialect: "jinan", langs: []string{"zh"}},
	"zh-qingdao":      {name: tx("Qingdao", "青岛话"), dialect: "qingdao", langs: []string{"zh"}},
	"zh-shijiazhuang": {name: tx("Shijiazhuang", "石家庄话"), dialect: "shijiazhuang", langs: []string{"zh"}},
	"zh-gansu":        {name: tx("Gansu", "甘肃话"), dialect: "gansu", langs: []string{"zh"}},
	"zh-ningxia":      {name: tx("Ningxia", "宁夏话"), dialect: "ningxia", langs: []string{"zh"}},
	"zh-beijing":      {name: tx("Beijing", "京腔"), dialect: "beijing", langs: []string{"zh"}},
	"zh-tianjin":      {name: tx("Tianjin", "天津话"), dialect: "tianjin", langs: []string{"zh"}},
	"zh-canton":       {name: tx("Cantonese-accented", "广普"), dialect: "cantonese", langs: []string{"zh"}},
	"zh-taiwan":       {name: tx("Taiwanese Mandarin", "台湾腔"), dialect: "taiwanese", langs: []string{"zh"}},
	"zh-shanghai":     {name: tx("Shanghai", "上海口音"), dialect: "shanghai", langs: []string{"zh"}},

	"es": {name: tx("Spanish", "西班牙语"), langs: []string{"es"}, sample: "Hola, gracias por escuchar. Esta es una pequeña muestra de mi voz."},
	"fr": {name: tx("French", "法语"), langs: []string{"fr"}, sample: "Bonjour, et merci de votre écoute. Voici un court extrait de ma voix."},
	"de": {name: tx("German", "德语"), langs: []string{"de"}, sample: "Hallo und danke fürs Zuhören. Das ist eine kurze Probe meiner Stimme."},
	"it": {name: tx("Italian", "意大利语"), langs: []string{"it"}, sample: "Ciao e grazie per l'ascolto. Questo è un breve esempio della mia voce."},
	"pt": {name: tx("Portuguese", "葡萄牙语"), langs: []string{"pt"}, sample: "Olá, obrigado por ouvir. Esta é uma pequena amostra da minha voz."},
	"ja": {name: tx("Japanese", "日语"), langs: []string{"ja"}, sample: "こんにちは。聞いてくれてありがとう。これは私の声のサンプルです。"},
	"ko": {name: tx("Korean", "韩语"), langs: []string{"ko"}, sample: "안녕하세요. 들어 주셔서 감사합니다. 제 목소리 샘플입니다."},
	"ru": {name: tx("Russian", "俄语"), langs: []string{"ru"}, sample: "Здравствуйте и спасибо, что слушаете. Это короткий образец моего голоса."},
	"hi": {name: tx("Hindi", "印地语"), langs: []string{"hi"}, sample: "नमस्ते, सुनने के लिए धन्यवाद। यह मेरी आवाज़ का एक छोटा नमूना है।"},
}

// person names a gender × age in archetype names.
func person(gender, age string) vd.Text {
	g := 0
	if gender == "female" {
		g = 1
	}
	t := map[string][2]vd.Text{
		"child":       {tx("boy", "小男孩"), tx("girl", "小女孩")},
		"teen":        {tx("teen boy", "少年"), tx("teen girl", "少女")},
		"young_adult": {tx("young man", "青年男声"), tx("young woman", "青年女声")},
		"middle_aged": {tx("man", "中年男声"), tx("woman", "中年女声")},
		"elderly":     {tx("older man", "老年男声"), tx("older woman", "老年女声")},
	}
	return t[age][g]
}
