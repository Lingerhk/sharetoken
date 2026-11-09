package main

import "strings"

// trayText is the native menu's text in one language. The page's dictionaries
// (client/ui/i18n/client) are not available here, because the native menu has
// to work when the page cannot load. Connected, Connecting and Retrying repeat
// the page's own words, and a test compares them. {app} is the app's name.
type trayText struct {
	SignedOut, Expired, Connecting, Retrying, Connected, ServiceDown, Open, Quit string
}

func (t trayText) fill(s string) string { return strings.ReplaceAll(s, "{app}", windowTitle()) }

var trayTexts = map[string]trayText{
	"en":    {"Not signed in", "Sign-in expired", "Connecting to the network…", "Can't reach the network. Retrying", "Connected", "The background service is not running", "Open {app}", "Quit {app}"},
	"zh-CN": {"未登录", "登录已失效", "正在连接网络…", "暂时连不上网络，正在重试", "已连接", "后台服务未运行", "打开 {app}", "退出 {app}"},
	"zh-TW": {"未登入", "登入已失效", "正在連線到網路…", "暫時連不上網路，正在重試", "已連線", "背景服務未執行", "開啟 {app}", "結束 {app}"},
	"es":    {"Sin iniciar sesión", "La sesión ha caducado", "Conectando con la red…", "No se puede conectar con la red. Reintentando", "Conectado", "El servicio en segundo plano no está en ejecución", "Abrir {app}", "Salir de {app}"},
	"fr":    {"Non connecté au compte", "Session expirée", "Connexion au réseau…", "Réseau injoignable. Nouvelle tentative en cours", "Connecté", "Le service d'arrière-plan n'est pas en cours d'exécution", "Ouvrir {app}", "Quitter {app}"},
	"de":    {"Nicht angemeldet", "Anmeldung abgelaufen", "Verbinde mit dem Netzwerk…", "Netzwerk nicht erreichbar. Neuer Versuch läuft", "Verbunden", "Der Hintergrunddienst läuft nicht", "{app} öffnen", "{app} beenden"},
	"pt-BR": {"Sessão não iniciada", "A sessão expirou", "Conectando à rede…", "Não foi possível acessar a rede. Tentando novamente", "Conectado", "O serviço em segundo plano não está em execução", "Abrir {app}", "Sair do {app}"},
	"ru":    {"Вход не выполнен", "Сеанс истёк", "Подключение к сети…", "Нет связи с сетью. Повторяем попытку", "Подключено", "Фоновая служба не запущена", "Открыть {app}", "Завершить {app}"},
	"ja":    {"未ログイン", "ログインの有効期限が切れました", "ネットワークに接続しています…", "ネットワークに接続できません。再試行しています", "接続済み", "バックグラウンドサービスが実行されていません", "{app} を開く", "{app} を終了"},
	"ko":    {"로그인되지 않음", "로그인이 만료됨", "네트워크에 연결하는 중…", "네트워크에 연결할 수 없습니다. 다시 시도하는 중입니다", "연결됨", "백그라운드 서비스가 실행되고 있지 않습니다", "{app} 열기", "{app} 종료"},
	"ar":    {"لم يتم تسجيل الدخول", "انتهت صلاحية تسجيل الدخول", "جارٍ الاتصال بالشبكة…", "تعذّر الاتصال بالشبكة، جارٍ إعادة المحاولة", "متصل", "خدمة الخلفية لا تعمل", "فتح {app}", "إنهاء {app}"},
	"hi":    {"साइन इन नहीं है", "साइन-इन की अवधि समाप्त हो गई", "नेटवर्क से कनेक्ट हो रहा है…", "अभी नेटवर्क से कनेक्ट नहीं हो पा रहा। दोबारा कोशिश हो रही है", "कनेक्टेड", "बैकग्राउंड सेवा नहीं चल रही है", "{app} खोलें", "{app} बंद करें"},
	"bn":    {"সাইন ইন করা নেই", "সাইন-ইনের মেয়াদ শেষ", "নেটওয়ার্কে সংযোগ হচ্ছে…", "এই মুহূর্তে নেটওয়ার্কে সংযোগ হচ্ছে না, আবার চেষ্টা করা হচ্ছে", "সংযুক্ত", "ব্যাকগ্রাউন্ড পরিষেবা চলছে না", "{app} খুলুন", "{app} বন্ধ করুন"},
	"ur":    {"سائن ان نہیں", "سائن اِن کی میعاد ختم ہو گئی", "نیٹ ورک سے جڑ رہا ہے…", "ابھی نیٹ ورک سے رابطہ نہیں ہو رہا۔ دوبارہ کوشش جاری ہے", "جڑا ہوا", "پس منظر کی سروس نہیں چل رہی", "{app} کھولیں", "{app} بند کریں"},
	"id":    {"Belum masuk", "Sesi masuk telah berakhir", "Menghubungkan ke jaringan…", "Tidak dapat menjangkau jaringan. Mencoba lagi", "Terhubung", "Layanan latar belakang tidak berjalan", "Buka {app}", "Keluar dari {app}"},
	"vi":    {"Chưa đăng nhập", "Phiên đăng nhập đã hết hạn", "Đang kết nối mạng…", "Hiện chưa kết nối được mạng, đang thử lại", "Đã kết nối", "Dịch vụ nền không chạy", "Mở {app}", "Thoát {app}"},
	"tr":    {"Oturum açılmadı", "Oturumun süresi doldu", "Ağa bağlanılıyor…", "Ağa ulaşılamıyor. Yeniden deneniyor", "Bağlı", "Arka plan hizmeti çalışmıyor", "{app} uygulamasını aç", "{app} uygulamasından çık"},
	"it":    {"Accesso non effettuato", "Accesso scaduto", "Connessione alla rete…", "Impossibile raggiungere la rete. Riprovo", "Connesso", "Il servizio in background non è in esecuzione", "Apri {app}", "Esci da {app}"},
	"pl":    {"Nie zalogowano", "Logowanie wygasło", "Łączenie z siecią…", "Chwilowo brak połączenia z siecią. Ponawianie", "Połączono", "Usługa w tle nie działa", "Otwórz {app}", "Zakończ {app}"},
	"th":    {"ยังไม่ได้ลงชื่อเข้าใช้", "การลงชื่อเข้าใช้หมดอายุแล้ว", "กำลังเชื่อมต่อเครือข่าย…", "ตอนนี้เชื่อมต่อเครือข่ายไม่ได้ กำลังลองใหม่", "เชื่อมต่อแล้ว", "บริการเบื้องหลังไม่ทำงาน", "เปิด {app}", "ออกจาก {app}"},
}
