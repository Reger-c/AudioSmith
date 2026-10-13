package gallery

import (
	"slices"

	vd "github.com/hehehai/AudioSmith/internal/voicedesign"
)

// Preset is a starting point: on the Design page it replaces the recipe
// (Attrs, and Language for the script); on the Clone page its StyleHint is
// the delivery instruction for engines that take one. Demo presets also
// get a "hear demo" card with Script.
type Preset struct {
	ID          string        `json:"id"`
	Icon        string        `json:"icon"` // semantic icon name for the UI ("book", "mic"…)
	Name        vd.Text       `json:"name"`
	Description vd.Text       `json:"description"`
	Attrs       vd.Attributes `json:"attrs"`
	StyleHint   vd.Text       `json:"styleHint"` // delivery instruction, for clones
	Script      vd.Text       `json:"script"`    // demo line
	Language    string        `json:"language"`  // the script's language for the demo ("en"/"zh")
	Demo        bool          `json:"demo,omitempty"`
}

var presets = []Preset{
	{ID: "narrator", Icon: "book", Name: tx("Narrator", "旁白"),
		Description: tx("Calm, low, unhurried: audiobooks and long reads.", "沉稳低缓，适合有声书与长文朗读。"),
		Attrs:       vd.Attributes{Age: "middle_aged", Pitch: "low", Styles: []string{"warm", "calm"}, Pace: "slow"},
		StyleHint:   tx("Read calmly and slowly, like an audiobook narrator.", "像有声书旁白一样，沉稳、缓慢地朗读。"),
		Script:      tx("The house at the end of the lane had been empty for years, until the night its windows lit up.", "巷子尽头那栋房子空了很多年，直到那天夜里，它的窗户亮了。"),
		Language:    "en", Demo: true},
	{ID: "conversational", Icon: "chat", Name: tx("Conversational", "聊天"),
		Description: tx("Relaxed and natural, like talking to a friend.", "放松自然，像和朋友聊天。"),
		Attrs:       vd.Attributes{Age: "young_adult", Pitch: "moderate", Styles: []string{"friendly"}},
		StyleHint:   tx("Speak casually and naturally, like chatting with a friend.", "随意自然地说，像和朋友聊天一样。"),
		Script:      tx("Honestly? I didn't think the recipe would work either, but here we are.", "说实话，我也没想到这个方子能成，结果还真成了。"),
		Language:    "en"},
	{ID: "newsreader", Icon: "news", Name: tx("Newsreader", "新闻播报"),
		Description: tx("Clear, neutral and authoritative.", "清晰、中立、有权威感。"),
		Attrs:       vd.Attributes{Age: "middle_aged", Pitch: "moderate", Styles: []string{"crisp", "professional"}, Emotion: "neutral"},
		StyleHint:   tx("Deliver it like a newsreader: clear, neutral and steady.", "像新闻主播一样播报：清晰、中立、平稳。"),
		Script:      tx("Good evening. Tonight: a new bridge, an old library, and the weather for the weekend.", "观众朋友们晚上好。今晚关注：新大桥、老图书馆，以及周末天气。"),
		Language:    "zh", Demo: true},
	{ID: "storyteller", Icon: "sparkles", Name: tx("Storyteller", "讲故事"),
		Description: tx("Warm and expressive, for tales told aloud.", "温暖而有表现力，适合讲故事。"),
		Attrs:       vd.Attributes{Age: "elderly", Styles: []string{"warm", "dramatic"}, Pace: "slow"},
		StyleHint:   tx("Tell it like a storyteller by the fire, with warmth and drama.", "像围炉讲故事的人那样，温暖又有起伏地讲。"),
		Script:      tx("And when the last candle went out, the old clock began, very softly, to sing.", "最后一支蜡烛熄灭时，那座老钟开始轻轻地唱起歌来。"),
		Language:    "en", Demo: true},
	{ID: "presenter", Icon: "briefcase", Name: tx("Presenter", "商务演示"),
		Description: tx("Confident and polished, for business and product talks.", "自信干练，适合商务与产品介绍。"),
		Attrs:       vd.Attributes{Age: "young_adult", Pitch: "moderate", Styles: []string{"crisp", "authoritative"}},
		StyleHint:   tx("Present confidently and clearly, like a product keynote.", "像产品发布会一样，自信清晰地讲。"),
		Script:      tx("Three things changed this year, and the third one is why we're here today.", "今年有三件事发生了变化，而第三件，正是我们今天来到这里的原因。"),
		Language:    "en"},
	{ID: "upbeat", Icon: "bolt", Name: tx("Upbeat", "元气"),
		Description: tx("Bright, high-energy and happy.", "明亮、高能量、开心。"),
		Attrs:       vd.Attributes{Age: "young_adult", Pitch: "high", Styles: []string{"bright", "energetic"}, Emotion: "happy"},
		StyleHint:   tx("Speak with lots of energy and a smile.", "充满活力、带着笑意地说。"),
		Script:      tx("Guess what? It's the weekend, and we've got a whole day of fun planned!", "猜猜怎么着？周末到啦，我们安排了一整天的好玩项目！"),
		Language:    "zh"},
	{ID: "whisper", Icon: "moon", Name: tx("Whisper", "耳语"),
		Description: tx("Soft, close whisper.", "轻柔的贴耳低语。"),
		Attrs:       vd.Attributes{Age: "young_adult", Styles: []string{"whisper", "soft"}, Pace: "slow"},
		StyleHint:   tx("Whisper softly and slowly, close to the microphone.", "贴近麦克风，轻轻地、慢慢地耳语。"),
		Script:      tx("Shh. Everyone's asleep. Let's tiptoe down and see what's in the kitchen.", "嘘，大家都睡了。我们踮着脚下楼，看看厨房里有什么。"),
		Language:    "en", Demo: true},
	{ID: "kid", Icon: "balloon", Name: tx("Kid", "童声"),
		Description: tx("A playful, happy child.", "活泼开心的小朋友。"),
		Attrs:       vd.Attributes{Age: "child", Pitch: "high", Styles: []string{"playful"}, Emotion: "happy"},
		StyleHint:   tx("Speak playfully and excitedly, like a happy child.", "像开心的小朋友一样，俏皮兴奋地说。"),
		Script:      tx("I found a shell that sounds like the whole ocean! Listen!", "我捡到一个贝壳，里面能听见整片大海！你听！"),
		Language:    "zh"},
	{ID: "elder", Icon: "leaf", Name: tx("Elder", "长者"),
		Description: tx("Low, weathered and wise.", "低沉、沧桑、睿智。"),
		Attrs:       vd.Attributes{Age: "elderly", Pitch: "low", Styles: []string{"raspy", "warm"}, Pace: "slow"},
		StyleHint:   tx("Speak slowly and thoughtfully, like a wise old person.", "像睿智的老人一样，缓慢而若有所思地说。"),
		Script:      tx("I planted this tree the year your father was born. It has outlived three storms.", "这棵树是你爸出生那年我种的，它熬过了三场大风暴。"),
		Language:    "zh"},
	{ID: "villain", Icon: "flame", Name: tx("Villain", "反派"),
		Description: tx("Very low, smooth and menacing.", "极低沉、圆滑而危险。"),
		Attrs:       vd.Attributes{Age: "middle_aged", Pitch: "very_low", Styles: []string{"magnetic"}, Pace: "slow", Emotion: "mysterious"},
		StyleHint:   tx("Speak slowly and quietly, with quiet menace.", "缓慢、低声，带着克制的威胁感。"),
		Script:      tx("You may keep the map. I already know where it leads.", "地图你留着吧，它通向哪里，我早就知道了。"),
		Language:    "en", Demo: true},
	{ID: "northeast-buddy", Icon: "flag", Name: tx("Northeast Buddy", "东北老铁"),
		Description: tx("Cheerful young man with a Northeastern Chinese accent.", "开朗的东北小伙。"),
		Attrs:       vd.Attributes{Gender: "male", Age: "young_adult", Styles: []string{"energetic", "friendly"}, Emotion: "happy", Dialect: "northeastern"},
		StyleHint:   tx("Speak cheerfully and loudly, in a Northeastern Chinese manner.", "用东北话的腔调，热情爽朗地说。"),
		Script:      tx("Hey, come in, come in! The stew is ready, grab a bowl!", "哎呀妈呀，快进屋！炖菜好了，整一碗！"),
		Language:    "zh", Demo: true},
	{ID: "calm-guide", Icon: "drop", Name: tx("Calm Guide", "冥想引导"),
		Description: tx("Soft, breathy and slow: meditation and sleep.", "轻柔、带气声、缓慢：冥想与助眠。"),
		Attrs:       vd.Attributes{Pitch: "moderate", Styles: []string{"breathy", "soft", "calm"}, Pace: "slow"},
		StyleHint:   tx("Guide gently and slowly, leaving space between sentences.", "轻柔缓慢地引导，句与句之间留出停顿。"),
		Script:      tx("Notice your breath. You don't need to change it. Just notice.", "留意你的呼吸。不需要改变它，只是留意。"),
		Language:    "en"},
}

// Presets lists the starting points in display order.
func Presets() []Preset {
	out := slices.Clone(presets)
	for i := range out {
		out[i].Attrs = out[i].Attrs.Canonical()
	}
	return out
}

// PresetByID finds a preset.
func PresetByID(id string) (Preset, bool) {
	for _, p := range Presets() {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// Recipe is the preset as a design recipe with the given seed.
func (p Preset) Recipe(seed int64) vd.Recipe {
	return vd.Recipe{Attrs: p.Attrs.Canonical(), Seed: seed}
}
