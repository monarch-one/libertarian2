// ANCAP WEB - A Libertarian RSS Reader
// =================================
//
// DESARROLLO CON AIR HOT RELOADING:
// - Para desarrollo usar: air (hot reloading automático)
// - Para producción usar: go run main.go
// - Configuración en: .air.toml
// - Script rápido: dev-hot.bat
//
// CARACTERÍSTICAS:
// - Sistema de autenticación con sesiones
// - Lector RSS con cache y paralelización
// - Interfaz terminal/retro con JetBrains Mono
// - Navegación por teclado (F1-F4, flechas, Space, etc.)
// - Funciones: guardar, favoritos, compartir
// - Scraping de contenido completo de artículos
//
// CREDENCIALES POR DEFECTO:
// - admin / admin123
// - ancap / libertad

package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"
)

type Feed struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

type Article struct {
	Title       string
	Link        string
	Date        string
	Source      string
	Description string
	IsFav       bool
}

type FavoriteArticle struct {
	Title  string `json:"title"`
	Link   string `json:"link"`
	Date   string `json:"date"`
	Source string `json:"source"`
}

type User struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type Session struct {
	Username string
	Expires  time.Time
}

type TemplateData struct {
	Articles      []Article
	ImportMessage string
}

type OPML struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    Head     `xml:"head"`
	Body    Body     `xml:"body"`
}

type Head struct {
	Title string `xml:"title"`
}

type Body struct {
	Outlines []Outline `xml:"outline"`
}

type Outline struct {
	Text     string    `xml:"text,attr"`
	Title    string    `xml:"title,attr"`
	Type     string    `xml:"type,attr"`
	XMLURL   string    `xml:"xmlUrl,attr"`
	HTMLURL  string    `xml:"htmlUrl,attr"`
	Outlines []Outline `xml:"outline"`
}

type CachedFeed struct {
	Articles  []Article
	LastFetch time.Time
	URL       string
}

type CachedArticleContent struct {
	Content   string
	Timestamp time.Time
	Success   bool
}

type FeedCache struct {
	mutex sync.RWMutex
	feeds map[string]CachedFeed
}

type ArticleContentCache struct {
	mutex    sync.RWMutex
	articles map[string]CachedArticleContent
}

var globalCache = &FeedCache{
	feeds: make(map[string]CachedFeed),
}

var articleContentCache = &ArticleContentCache{
	articles: make(map[string]CachedArticleContent),
}

var sessions = make(map[string]Session)
var sessionMutex sync.RWMutex

const CACHE_DURATION = 1 * time.Minute
const SESSION_DURATION = 24 * time.Hour

func loadUsers() []User {
	file, err := os.Open("users.json")
	if err != nil {
		// Si no existe users.json, crear usuario por defecto
		defaultUsers := []User{
			{"admin", "admin123"},
			{"ancap", "libertad"},
		}
		saveUsers(defaultUsers)
		return defaultUsers
	}
	defer file.Close()

	var users []User
	json.NewDecoder(file).Decode(&users)
	return users
}

func saveUsers(users []User) error {
	file, err := os.Create("users.json")
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewEncoder(file).Encode(users)
}

func validateLogin(username, password string) bool {
	users := loadUsers()
	for _, user := range users {
		if user.Username == username && user.Password == password {
			return true
		}
	}
	return false
}

func generateSessionID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func createSession(username string) string {
	sessionMutex.Lock()
	defer sessionMutex.Unlock()

	sessionID := generateSessionID()
	sessions[sessionID] = Session{
		Username: username,
		Expires:  time.Now().Add(SESSION_DURATION),
	}
	return sessionID
}

func validateSession(sessionID string) (string, bool) {
	sessionMutex.RLock()
	defer sessionMutex.RUnlock()

	session, exists := sessions[sessionID]
	if !exists || time.Now().After(session.Expires) {
		return "", false
	}
	return session.Username, true
}

func clearExpiredSessions() {
	sessionMutex.Lock()
	defer sessionMutex.Unlock()

	for id, session := range sessions {
		if time.Now().After(session.Expires) {
			delete(sessions, id)
		}
	}
}

func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Si es la página de login, permitir acceso
		if r.URL.Path == "/login" || r.URL.Path == "/api/login" {
			next.ServeHTTP(w, r)
			return
		}

		// Verificar sesión
		cookie, err := r.Cookie("session_id")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
			return
		}

		username, valid := validateSession(cookie.Value)
		if !valid {
			http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
			return
		}

		// Agregar username al contexto si es necesario
		r.Header.Set("X-Username", username)
		next.ServeHTTP(w, r)
	})
}

func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		gzw := &gzipResponseWriter{ResponseWriter: w, Writer: gz}
		next.ServeHTTP(gzw, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	Writer io.Writer
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

func loadFeeds() []Feed {
	file, err := os.Open("feeds.json")
	if err != nil {
		// Si no existe feeds.json, crear algunos feeds de prueba
		return []Feed{
			{"https://feeds.feedburner.com/oreilly/radar", true},
			{"https://rss.cnn.com/rss/edition.rss", true},
		}
	}
	defer file.Close()

	var feeds []Feed
	json.NewDecoder(file).Decode(&feeds)
	return feeds
}

func saveFeed(feed Feed) error {
	feeds := loadFeeds()
	for _, f := range feeds {
		if f.URL == feed.URL {
			return nil
		}
	}
	feeds = append(feeds, feed)

	file, err := os.Create("feeds.json")
	if err != nil {
		return err
	}
	defer file.Close()

	return json.NewEncoder(file).Encode(feeds)
}

func loadFavoriteArticles() []FavoriteArticle {
	file, err := os.Open("favorites.json")
	if err != nil {
		return []FavoriteArticle{}
	}
	defer file.Close()

	var favorites []FavoriteArticle
	json.NewDecoder(file).Decode(&favorites)
	return favorites
}

func saveFavoriteArticle(article FavoriteArticle) {
	favorites := loadFavoriteArticles()

	for i, fav := range favorites {
		if fav.Link == article.Link {
			favorites = append(favorites[:i], favorites[i+1:]...)
			file, err := os.Create("favorites.json")
			if err != nil {
				return
			}
			defer file.Close()
			json.NewEncoder(file).Encode(favorites)
			return
		}
	}

	favorites = append(favorites, article)
	file, err := os.Create("favorites.json")
	if err != nil {
		return
	}
	defer file.Close()
	json.NewEncoder(file).Encode(favorites)
}

func isArticleFavorite(link string) bool {
	favorites := loadFavoriteArticles()
	for _, fav := range favorites {
		if fav.Link == link {
			return true
		}
	}
	return false
}

func getCachedOrFetch(feedURL string) []Article {
	globalCache.mutex.RLock()
	cached, exists := globalCache.feeds[feedURL]
	globalCache.mutex.RUnlock()
	if exists && time.Since(cached.LastFetch) < CACHE_DURATION {
		log.Printf("🟢 Cache HIT para %s (edad: %v)", feedURL, time.Since(cached.LastFetch))
		return cached.Articles
	}
	log.Printf("🔴 Cache MISS para %s - fetching...", feedURL)
	articles := fetchFeedArticles(feedURL)
	globalCache.mutex.Lock()
	globalCache.feeds[feedURL] = CachedFeed{
		Articles:  articles,
		LastFetch: time.Now(),
		URL:       feedURL,
	}
	globalCache.mutex.Unlock()
	return articles
}

func renderHomePage(w http.ResponseWriter, data TemplateData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")

	html := `<!DOCTYPE html>
<html lang="es">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>ANCAP - ` + time.Now().Format("15:04:05") + ` [v2.2]</title>
    <style>
        @font-face {
            font-family: 'JetBrains Mono';
            src: url('/static/fonts/JetBrainsMonoNerdFont-Regular.woff2') format('woff2'),
                 url('/static/fonts/JetBrainsMono/JetBrainsMonoNerdFont-Regular.ttf') format('truetype');
            font-weight: 400;
            font-style: normal;
            font-display: swap;
        }
        @font-face {
            font-family: 'JetBrains Mono';
            src: url('/static/fonts/JetBrainsMono/JetBrainsMonoNerdFont-Light.ttf') format('truetype');
            font-weight: 300;
            font-style: normal;
            font-display: swap;
        }
        body { 
            background: #000; 
            color: #00ff00; 
            font-family: 'JetBrains Mono', 'Courier New', monospace; 
            font-size: 14px; 
            font-weight: 300;
            margin: 0; 
            padding: 20px;
            line-height: 1.2;
            display: flex;
            justify-content: center;
        }
        
        .container { 
            max-width: 720px; 
            margin: 0; 
            text-align: left;
            width: 100%;
            padding-top: 140px;
            overflow: hidden;
            position: relative;
        }
        .main-header {
            position: fixed;
            top: 0;
            left: 50%;
            transform: translateX(-50%);
            width: 100%;
            max-width: 720px;
            background: #000;
            z-index: 1000;
            margin-bottom: 20px;
            padding: 10px 20px;
            border-bottom: 1px solid #333;
            box-sizing: border-box;
        }
        .datetime-display {
            position: absolute;
            top: 25px;
            right: 10px;
            font-size: 11px;
            color: #00aa00;
            font-family: 'JetBrains Mono', monospace;
            font-weight: 300;
            text-align: right;
            line-height: 1.2;
        }
        .ascii-logo {
            font-size: 12px;
            color: #00ff00;
            font-family: 'Courier New', monospace;
            white-space: pre;
            line-height: 1.0;
            text-align: center;
        }
        .subtitle {
            font-size: 10px;
            color: #888;
            text-align: center;
            margin-top: 5px;
        }
        .tabs {
            display: flex;
            width: 100%;
            max-width: 720px;
            margin-bottom: 20px;
            position: fixed;
            top: 100px;
            left: 50%;
            transform: translateX(-50%);
            background: #000;
            z-index: 1100;
            padding: 0 20px;
            box-sizing: border-box;
        }
        .tabs::before {
            content: '';
            position: absolute;
            top: -20px;
            left: 0;
            right: 0;
            height: 20px;
            background: #000;
            z-index: 1101;
        }
        .tab {
            flex: 1;
            text-align: center;
            padding: 10px;
            color: #00ff00;
            text-decoration: none;
            border: 1px dashed #00ff00;
            border-radius: 8px 8px 0 0;
            background: #000;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-size: 12px;
            cursor: pointer;
            margin: 2px 2px 0 2px;
            transition: all 0.3s ease;
            backdrop-filter: blur(5px);
            position: relative;
        }
        .tab:hover {
            background: #000;
            transform: scale(1.02);
        }
        .tab.tab-active {
            background: #000;
            color: #00ff00;
            font-weight: bold;
            border-color: #00ff00;
            border-bottom: 1px solid #000;
            z-index: 1001;
        }
        .tab.tab-active::after {
            content: '';
            position: absolute;
            bottom: -1px;
            left: 0;
            right: 0;
            height: 2px;
            background: #000;
            z-index: 1002;
        }
        h1 { 
            color: #00ff00; 
            margin-bottom: 20px; 
            font-size: 14px;
            text-align: left;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-weight: 300;
            line-height: 1.2;
        }
        .info { 
            margin-bottom: 20px; 
            color: #00ff00; 
            font-size: 14px;
            text-align: left;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-weight: 300;
            line-height: 1.2;
        }
        .article-line { 
            display: flex;
            align-items: center;
            padding: 2px 0;
            margin-bottom: 0px;
            white-space: nowrap;
            overflow: hidden;
            text-overflow: ellipsis;
            max-width: 100%;
            height: 18px;
            line-height: 1.2;
        }
        .article-line:hover {
            background: #111;
        }
        .full-line-link {
            color: #00ff00;
            text-decoration: none;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-weight: 300;
            font-size: 14px;
            line-height: 1.2;
            display: flex;
            align-items: center;
            width: 100%;
            overflow: hidden;
            text-overflow: ellipsis;
            white-space: nowrap;
        }
        .full-line-link:hover {
            text-decoration: none;
        }
        a {
            color: #ffff00;
            text-decoration: none;
        }
        a:hover {
            text-decoration: none;
            color: #ffff88;
        }
        .meta { 
            color: #00ff00; 
            font-size: 14px;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-weight: 300;
            flex-shrink: 0;
            white-space: nowrap;
            margin-right: 0px;
            line-height: 1.2;
        }
        .title { 
            color: #00ff00; 
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-weight: 300;
            flex-shrink: 1;
            overflow: hidden;
            text-overflow: ellipsis;
            white-space: nowrap;
            font-size: 14px;
            line-height: 1.2;
            margin-left: 5px;
        }
        .article-content {
            display: none;
            background: #000;
            color: #00ff00;
            padding: 15px;
            margin: 5px 0;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-size: 14px;
            line-height: 1.4;
            white-space: normal;
            word-wrap: break-word;
            max-height: 500px;
            overflow-y: auto;
            overflow-x: hidden;
            position: relative;
        }
        .article-content.expanded {
            display: block;
        }
        .article-container {
            margin-bottom: 1px;
        }
        .article-line {
            cursor: pointer;
        }
        .article-line.selected {
            background: #222;
        }
        .article-line.read {
            color: #666 !important;
        }
        .article-line.read .meta {
            color: #666 !important;
        }
        .article-line.read .title {
            color: #666 !important;
        }
        .article-line.read .meta span {
            color: #666 !important;
        }
        
        /* Modal/Window styles */
        .modal {
            display: none;
            position: fixed;
            z-index: 1000;
            left: 0;
            top: 0;
            width: 100%;
            height: 100%;
            background: rgba(0, 0, 0, 0.8);
        }
        .modal.active {
            display: block;
        }
        .modal-content {
            background: #000;
            margin: 5% auto;
            padding: 20px;
            border: 2px solid #00ff00;
            width: 80%;
            max-width: 800px;
            color: #00ff00;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-size: 14px;
            position: relative;
            max-height: 80vh;
            overflow-y: auto;
        }
        .modal-header {
            font-size: 18px;
            font-weight: bold;
            margin-bottom: 20px;
            border-bottom: 1px solid #00ff00;
            padding-bottom: 10px;
        }
        .close {
            color: #00ff00;
            float: right;
            font-size: 28px;
            font-weight: bold;
            cursor: pointer;
            position: absolute;
            right: 15px;
            top: 10px;
        }
        .close:hover {
            color: #fff;
        }
        .config-section {
            margin: 15px 0;
            padding: 10px;
            border: 1px solid #333;
        }
        .config-label {
            display: block;
            margin: 5px 0;
        }
        .config-input, .config-select {
            background: #000;
            color: #00ff00;
            border: 1px solid #00ff00;
            font-family: 'JetBrains Mono', monospace;
            font-size: 12px;
            padding: 3px 5px;
            width: 200px;
        }
        .tab {
            position: relative;
        }
        .tab-shortcut {
            font-size: 10px;
            color: #888;
            margin-left: 5px;
        }
        
        /* Page content styles */
        .page-content {
            width: 100%;
            color: #00ff00;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-size: 14px;
            padding: 20px 0;
        }
        .page-header {
            font-size: 18px;
            font-weight: bold;
            margin-bottom: 20px;
            border-bottom: 1px solid #00ff00;
            padding-bottom: 10px;
            color: #00ff00;
        }
    </style>
    <script>
        let selectedIndex = -1;
        const articles = [];
        let currentPage = 'feeds'; // Variable para rastrear la página activa
        
        function toggleArticle(index, event) {
            if (event) {
                // Si es doble clic o Ctrl+clic, abrir enlace
                if (event.detail === 2 || event.ctrlKey) {
                    window.open(articles[index].link, '_blank');
                    return;
                }
                event.preventDefault();
                event.stopPropagation();
            }
            
            const content = document.getElementById('content-' + index);
            const isCurrentlyExpanded = content.classList.contains('expanded');
            
            // Cerrar todos los artículos
            document.querySelectorAll('.article-content').forEach(el => {
                el.classList.remove('expanded');
            });
            
            // Abrir el seleccionado si no estaba abierto
            if (!isCurrentlyExpanded) {
                content.classList.add('expanded');
                selectedIndex = index;
                
                // Marcar artículo como leído
                const articleLine = document.querySelector('.article-line[onclick*="toggleArticle(' + index + ',"]');
                if (articleLine) {
                    articleLine.classList.add('read');
                    articles[index].isRead = true;
                }
                
                // Mover el artículo expandido al primer lugar
                moveArticleToTop(index);
                
                // Scroll al principio de la página
                window.scrollTo({
                    top: 0,
                    behavior: 'smooth'
                });
                
                // Cargar contenido completo sin límites
                loadArticleContent(index);
            }
        }
        
        function moveArticleToTop(expandedIndex) {
            const feedsContent = document.getElementById('feeds-content');
            const articleLines = feedsContent.querySelectorAll('.article-line');
            
            if (expandedIndex >= 0 && expandedIndex < articleLines.length) {
                const expandedArticle = articleLines[expandedIndex];
                const parentDiv = expandedArticle.parentNode;
                
                // Mover el artículo expandido al principio
                parentDiv.insertBefore(expandedArticle, parentDiv.firstChild);
                
                // Reordenar los índices en el array articles y actualizar los IDs
                const movedArticle = articles.splice(expandedIndex, 1)[0];
                articles.unshift(movedArticle);
                
                // Actualizar todos los índices en los onclick y IDs
                updateArticleIndices();
                
                // Actualizar selectedIndex para que apunte al primer elemento
                selectedIndex = 0;
            }
        }
        
        function updateArticleIndices() {
            const feedsContent = document.getElementById('feeds-content');
            const articleLines = feedsContent.querySelectorAll('.article-line');
            
            articleLines.forEach((line, newIndex) => {
                // Actualizar onclick
                line.setAttribute('onclick', 'toggleArticle(' + newIndex + ', event)');
                
                // Actualizar ID del contenido
                const content = line.querySelector('.article-content');
                if (content) {
                    content.id = 'content-' + newIndex;
                }
                
                // Actualizar botones
                const buttons = line.querySelectorAll('[onclick*="saveArticle"], [onclick*="loveArticle"], [onclick*="shareArticle"]');
                buttons.forEach(button => {
                    const onclick = button.getAttribute('onclick');
                    if (onclick.includes('saveArticle')) {
                        button.setAttribute('onclick', 'saveArticle(' + newIndex + ', event)');
                    } else if (onclick.includes('loveArticle')) {
                        button.setAttribute('onclick', 'loveArticle(' + newIndex + ', event)');
                    } else if (onclick.includes('shareArticle')) {
                        button.setAttribute('onclick', 'shareArticle(' + newIndex + ', event)');
                    }
                });
            });
        }
        
        // Función mejorada para cargar contenido completo del artículo
        function loadArticleContent(index) {
            const article = articles[index];
            const content = document.getElementById('content-' + index);
            
            content.innerHTML = 'Loading full content...';
            
            // Intentar obtener el contenido completo del artículo
            fetchFullArticleContent(article.link)
                .then(fullContent => {
                    displayArticleContent(content, article, fullContent, index);
                })
                .catch(() => {
                    // Fallback al contenido de descripción
                    displayArticleContent(content, article, null, index);
                });
        }
        
        // Función para obtener contenido completo del artículo
        async function fetchFullArticleContent(url) {
            try {
                // Intentar hacer scraping del contenido completo
                const response = await fetch('/api/scrape-article', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json',
                    },
                    body: JSON.stringify({ url: url })
                });
                
                if (response.ok) {
                    const data = await response.json();
                    return data.content;
                } else {
                    throw new Error('No se pudo obtener el contenido completo');
                }
            } catch (error) {
                console.log('Fallback a descripción RSS:', error);
                throw error;
            }
        }
        
        // Función para mostrar el contenido del artículo
        function displayArticleContent(content, article, fullContent, index) {
            // Usar contenido completo si está disponible, sino usar descripción
            let articleText = fullContent || article.description || 'Content not available.';
            
            // Limpiar HTML pero preservar estructura de párrafos
            if (!fullContent) {
                // Convertir elementos de bloque a saltos de línea antes de limpiar
                articleText = articleText.replace(/<\/p>/gi, '\n\n');
                articleText = articleText.replace(/<br\s*\/?>/gi, '\n');
                articleText = articleText.replace(/<\/div>/gi, '\n');
                articleText = articleText.replace(/<\/h[1-6]>/gi, '\n\n');
                
                // Limpiar etiquetas HTML
                articleText = articleText.replace(/<[^>]*>/g, '');
                articleText = articleText.replace(/&[^;]+;/g, ' ');
                articleText = articleText.trim();
                
                // NO limitar longitud - mostrar contenido completo
            } else {
                // Para contenido completo, preservar mejor la estructura
                articleText = articleText.replace(/<\/p>/gi, '\n\n');
                articleText = articleText.replace(/<br\s*\/?>/gi, '\n');
                articleText = articleText.replace(/<\/div>/gi, '\n');
                articleText = articleText.replace(/<\/h[1-6]>/gi, '\n\n');
                articleText = articleText.replace(/<[^>]*>/g, '');
                articleText = articleText.replace(/&[^;]+;/g, ' ');
                
                // Limpiar saltos de línea excesivos pero mantener párrafos
                articleText = articleText.replace(/\n\s*\n\s*\n/g, '\n\n');
                articleText = articleText.trim();
            }
            
            // Convertir saltos de línea a HTML para mostrar correctamente
            if (articleText.includes('\n\n')) {
                // Si hay párrafos dobles, tratarlos como párrafos separados
                const paragraphs = articleText.split('\n\n').filter(p => p.trim());
                articleText = paragraphs.map(p => '<p>' + p.replace(/\n/g, '<br>') + '</p>').join('');
            } else {
                // Si no hay párrafos dobles, solo reemplazar saltos de línea simples
                articleText = '<p>' + articleText.replace(/\n/g, '<br>') + '</p>';
            }
            
            // Determinar posición de botones según configuración
            const buttonsPosition = localStorage.getItem('buttonsPosition') || 'right';
            
            const buttonStyle = buttonsPosition === 'right' ? 
                'position: absolute; right: 15px; top: 15px; text-align: right;' :
                'position: absolute; left: 15px; top: 15px; text-align: left;';
            
            const buttonsHTML = '<div style="' + buttonStyle + '">' +
                '<div id="saved-btn-' + index + '" onclick="toggleSaved(' + index + ', event)" style="color: ' + (article.isSaved ? '#FFD700' : '#00ff00') + '; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">' + (article.isSaved ? '[SAVED]' : '[SAVE S]') + '</div>' +
                '<div id="loved-btn-' + index + '" onclick="toggleLoved(' + index + ')" style="color: ' + (article.isLoved ? '#FF69B4' : '#00ff00') + '; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">' + (article.isLoved ? '[LOVED]' : '[LOVE L]') + '</div>' +
                '<div onclick="shareArticle(' + index + ')" style="color: #00ff00; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">[SHARE C]</div>' +
                '<div onclick="window.open(\'' + article.link + '\', \'_blank\')" style="color: #ffff00; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px;">[ORIGINAL O]</div>' +
                '</div>';
            
            // Extraer y procesar imágenes del contenido original
            let imageHTML = '';
            const originalDescription = article.description || '';
            
            if (originalDescription && originalDescription.trim().length > 0) {
                const imgRegex = /<img[^>]+src=["\']([^"\']+)["\'][^>]*>/gi;
                let match;
                
                const images = [];
                while ((match = imgRegex.exec(originalDescription)) !== null && images.length < 3) {
                    const imgSrc = match[1];
                    if (imgSrc && (imgSrc.startsWith('http://') || imgSrc.startsWith('https://') || imgSrc.startsWith('//'))) {
                        images.push(imgSrc);
                    }
                }
                
                if (images.length > 0) {
                    imageHTML = '<div style="margin-bottom: 15px; text-align: left;">';
                    images.forEach(imgSrc => {
                        imageHTML += '<img src="' + imgSrc + '" style="max-width: 280px; max-height: 200px; margin: 5px 10px 5px 0; border: 1px solid #333; object-fit: cover; display: none;" onerror="this.style.display=\'none\'" onload="this.style.display=\'inline-block\'">';
                    });
                    imageHTML += '</div>';
                }
            }
            
            const contentPadding = buttonsPosition === 'right' ? 
                'padding-right: 120px;' : 
                'padding-left: 120px;';
            
            content.innerHTML = buttonsHTML +
                              '<div style="' + contentPadding + '">' +
                              (imageHTML || '') +
                              '<div style="margin-bottom: 10px; color: #888; font-size: 12px;">' + article.date + ' | ' + article.source + '</div>' +
                              '<div style="margin-bottom: 15px;"><a href="' + article.link + '" target="_blank" style="color: #ffff00; text-decoration: none;">' + article.title + '</a></div>' +
                              '<div style="line-height: 1.6; color: #ccc; text-align: justify;">' + articleText + '</div>' +
                              (fullContent ? '<div style="margin-top: 15px; padding-top: 10px; border-top: 1px solid #333; color: #888; font-size: 11px;">FULL CONTENT LOADED</div>' : '<div style="margin-top: 15px; padding-top: 10px; border-top: 1px solid #333; color: #888; font-size: 11px;">RSS feed content. Double click on the line to view the complete article on the original site</div>') +
                              '</div>';
        }
        
        document.addEventListener('keydown', function(event) {
            // Usar la variable global currentPage en lugar de buscar en el DOM
            let totalArticles;
            let articleContainers;
            if (currentPage === 'feeds') {
                articleContainers = document.querySelectorAll('#feeds-content .article-container');
                totalArticles = articleContainers.length;
            } else if (currentPage === 'saved') {
                articleContainers = document.querySelectorAll('#saved-articles-list .article-container');
                totalArticles = articleContainers.length;
            } else if (currentPage === 'loved') {
                articleContainers = document.querySelectorAll('#loved-articles-list .article-container');
                totalArticles = articleContainers.length;
            } else {
                totalArticles = 0;
                articleContainers = [];
            }
            
            const expandedArticle = document.querySelector('.article-content.expanded');
            
            // Evitar atajos si hay un input activo
            if (event.target.tagName === 'INPUT' || event.target.tagName === 'TEXTAREA') {
                return;
            }
            
            switch(event.code) {
                case 'Space':
                    event.preventDefault();
                    if (selectedIndex >= 0 && totalArticles > 0) {
                        // Scroll al principio de la página antes de expandir
                        window.scrollTo({
                            top: 0,
                            behavior: 'smooth'
                        });
                        
                        // Expandir artículo según la página
                        if (currentPage === 'feeds') {
                            toggleArticle(selectedIndex);
                        } else if (currentPage === 'saved' || currentPage === 'loved') {
                            // Para saved/loved necesitamos usar un ID único basado en el link
                            const container = articleContainers[selectedIndex];
                            if (container) {
                                const link = container.querySelector('.full-line-link').getAttribute('data-link');
                                const prefix = currentPage === 'saved' ? 'content-saved-' : 'content-loved-';
                                const contentId = prefix + btoa(link).replace(/=/g, '').substring(0, 10);
                                toggleArticleGeneric(container, contentId, link);
                            }
                        }
                    }
                    break;
                    
                case 'ArrowDown':
                case 'KeyJ':
                    event.preventDefault();
                    if (expandedArticle) {
                        // Si hay artículo expandido, ir al siguiente y expandirlo SIN mover al top
                        if (selectedIndex < totalArticles - 1) {
                            // Cerrar artículo actual
                            document.querySelectorAll('.article-content').forEach(el => {
                                el.classList.remove('expanded');
                            });
                            
                            selectedIndex++;
                            highlightSelected();
                            
                            // Expandir el siguiente artículo SIN mover
                            if (currentPage === 'feeds') {
                                const content = document.getElementById('content-' + selectedIndex);
                                if (content) {
                                    content.classList.add('expanded');
                                    
                                    // Marcar artículo como leído
                                    const articleLine = document.querySelector('.article-line[onclick*="toggleArticle(' + selectedIndex + ',"]');
                                    if (articleLine) {
                                        articleLine.classList.add('read');
                                        articles[selectedIndex].isRead = true;
                                    }
                                    
                                    loadArticleContent(selectedIndex);
                                }
                            } else if (currentPage === 'saved' || currentPage === 'loved') {
                                const container = articleContainers[selectedIndex];
                                if (container) {
                                    const link = container.querySelector('.full-line-link').getAttribute('data-link');
                                    const prefix = currentPage === 'saved' ? 'content-saved-' : 'content-loved-';
                                    const contentId = prefix + btoa(link).replace(/=/g, '').substring(0, 10);
                                    toggleArticleGeneric(container, contentId, link, true);
                                }
                            }
                        }
                    } else {
                        // Navegación normal
                        if (selectedIndex < totalArticles - 1) {
                            selectedIndex++;
                            highlightSelected();
                        }
                    }
                    break;
                    
                case 'ArrowUp':
                case 'KeyK':
                    event.preventDefault();
                    if (expandedArticle) {
                        // Si hay artículo expandido, ir al anterior y expandirlo SIN mover al top
                        if (selectedIndex > 0) {
                            // Cerrar artículo actual
                            document.querySelectorAll('.article-content').forEach(el => {
                                el.classList.remove('expanded');
                            });
                            
                            selectedIndex--;
                            highlightSelected();
                            
                            // Expandir el anterior artículo SIN mover
                            if (currentPage === 'feeds') {
                                const content = document.getElementById('content-' + selectedIndex);
                                if (content) {
                                    content.classList.add('expanded');
                                    
                                    // Marcar artículo como leído
                                    const articleLine = document.querySelector('.article-line[onclick*="toggleArticle(' + selectedIndex + ',"]');
                                    if (articleLine) {
                                        articleLine.classList.add('read');
                                        articles[selectedIndex].isRead = true;
                                    }
                                    
                                    loadArticleContent(selectedIndex);
                                }
                            } else if (currentPage === 'saved' || currentPage === 'loved') {
                                const container = articleContainers[selectedIndex];
                                if (container) {
                                    const link = container.querySelector('.full-line-link').getAttribute('data-link');
                                    const prefix = currentPage === 'saved' ? 'content-saved-' : 'content-loved-';
                                    const contentId = prefix + btoa(link).replace(/=/g, '').substring(0, 10);
                                    toggleArticleGeneric(container, contentId, link, true);
                                }
                            }
                        }
                    } else {
                        // Navegación normal
                        if (selectedIndex > 0) {
                            selectedIndex--;
                            highlightSelected();
                        } else if (selectedIndex === -1 && totalArticles > 0) {
                            selectedIndex = 0;
                            highlightSelected();
                        }
                    }
                    break;
                    
                case 'Enter':
                    event.preventDefault();
                    if (selectedIndex >= 0 && totalArticles > 0) {
                        // Obtener el enlace según la página
                        let link;
                        if (currentPage === 'feeds') {
                            link = articles[selectedIndex].link;
                        } else if (currentPage === 'saved' || currentPage === 'loved') {
                            const container = articleContainers[selectedIndex];
                            if (container) {
                                link = container.querySelector('.full-line-link').getAttribute('data-link');
                            }
                        }
                        if (link) {
                            window.open(link, '_blank');
                        }
                    }
                    break;
                    
                case 'KeyS':
                    event.preventDefault();
                    if (selectedIndex >= 0 && totalArticles > 0) {
                        // Obtener el artículo actual según la página
                        if (currentPage === 'feeds') {
                            toggleSaved(selectedIndex);
                        } else if (currentPage === 'saved' || currentPage === 'loved') {
                            // Para saved/loved, obtenemos el artículo del DOM
                            const container = articleContainers[selectedIndex];
                            if (container) {
                                const link = container.querySelector('.full-line-link').getAttribute('data-link');
                                const originalIndex = articles.findIndex(a => a.link === link);
                                if (originalIndex >= 0) {
                                    toggleSaved(originalIndex);
                                }
                            }
                        }
                    }
                    break;
                    
                case 'KeyL':
                    event.preventDefault();
                    if (selectedIndex >= 0 && totalArticles > 0) {
                        // Obtener el artículo actual según la página
                        if (currentPage === 'feeds') {
                            toggleLoved(selectedIndex);
                        } else if (currentPage === 'saved' || currentPage === 'loved') {
                            // Para saved/loved, obtenemos el artículo del DOM
                            const container = articleContainers[selectedIndex];
                            if (container) {
                                const link = container.querySelector('.full-line-link').getAttribute('data-link');
                                const originalIndex = articles.findIndex(a => a.link === link);
                                if (originalIndex >= 0) {
                                    toggleLoved(originalIndex);
                                }
                            }
                        }
                    }
                    break;
                    
                case 'KeyC':
                    event.preventDefault();
                    if (selectedIndex >= 0) {
                        shareArticle(selectedIndex);
                    }
                    break;
                    
                case 'KeyO':
                    event.preventDefault();
                    if (selectedIndex >= 0 && totalArticles > 0) {
                        // Obtener el enlace según la página y abrirlo
                        let link;
                        if (currentPage === 'feeds') {
                            link = articles[selectedIndex].link;
                        } else if (currentPage === 'saved' || currentPage === 'loved') {
                            const container = articleContainers[selectedIndex];
                            if (container) {
                                link = container.querySelector('.full-line-link').getAttribute('data-link');
                            }
                        }
                        if (link) {
                            window.open(link, '_blank');
                        }
                    }
                    break;
                    
                case 'Home':
                    event.preventDefault();
                    selectedIndex = 0;
                    highlightSelected();
                    break;
                    
                case 'End':
                    event.preventDefault();
                    selectedIndex = totalArticles - 1;
                    highlightSelected();
                    break;
                    
                case 'Escape':
                    event.preventDefault();
                    // Cerrar todos los artículos expandidos y volver a feeds
                    document.querySelectorAll('.article-content').forEach(el => {
                        el.classList.remove('expanded');
                    });
                    showPage('feeds');
                    break;
                    
                case 'F1':
                    event.preventDefault();
                    showPage('feeds');
                    break;
                    
                case 'F2':
                    event.preventDefault();
                    showPage('saved');
                    break;
                    
                case 'F3':
                    event.preventDefault();
                    showPage('loved');
                    break;
                    
                case 'F4':
                    event.preventDefault();
                    showPage('config');
                    break;
            }
        });
        
        function highlightSelected() {
            // Quitar highlight previo de todas las páginas
            document.querySelectorAll('.article-line').forEach(el => {
                el.classList.remove('selected');
            });
            
            // Agregar highlight al seleccionado
            if (selectedIndex >= 0) {
                // Buscar en la página activa usando currentPage
                let lines;
                
                if (currentPage === 'feeds') {
                    lines = document.querySelectorAll('#feeds-content .article-line');
                } else if (currentPage === 'saved') {
                    lines = document.querySelectorAll('#saved-articles-list .article-line');
                } else if (currentPage === 'loved') {
                    lines = document.querySelectorAll('#loved-articles-list .article-line');
                }
                
                if (lines && lines[selectedIndex]) {
                    lines[selectedIndex].classList.add('selected');
                    
                    // Calcular la posición para que el elemento seleccionado esté en la primera línea visible
                    const element = lines[selectedIndex];
                    const headerHeight = 160; // Altura del header + pestañas
                    
                    // Obtener la posición absoluta del elemento en la página
                    const elementTop = element.offsetTop;
                    
                    // Scroll para que el elemento esté exactamente debajo del header (primera línea visible)
                    const scrollTarget = elementTop - headerHeight;
                    
                    window.scrollTo({
                        top: Math.max(0, scrollTarget), // Evitar scroll negativo
                        behavior: 'smooth'
                    });
                }
            }
        }
        
        function toggleArticleGeneric(container, contentId, link, forceOpen = false) {
            let content = container.querySelector('.article-content');
            if (!content) {
                content = document.createElement('div');
                content.className = 'article-content';
                content.id = contentId;
                container.appendChild(content);
            }
            
            const isCurrentlyExpanded = content.classList.contains('expanded');
            
            // Cerrar todos los artículos
            document.querySelectorAll('.article-content').forEach(el => {
                el.classList.remove('expanded');
            });
            
            // Abrir el seleccionado si no estaba abierto O si forceOpen es true
            if (!isCurrentlyExpanded || forceOpen) {
                content.classList.add('expanded');
                
                // Scroll para que el artículo aparezca después del header fijo
                setTimeout(() => {
                    container.scrollIntoView({ 
                        behavior: 'smooth', 
                        block: 'start' 
                    });
                    // Ajuste adicional para el header fijo
                    window.scrollBy(0, -180);
                }, 100);
                
                // Cargar contenido si no está cargado
                if (!content.innerHTML.trim()) {
                    content.innerHTML = 'Loading content...';
                    loadArticleContentGeneric(content, container, link);
                }
            }
        }
        
        function loadArticleContentGeneric(content, container, link) {
            // Obtener datos del artículo desde el DOM
            const titleElement = container.querySelector('.title');
            const metaElement = container.querySelector('.meta');
            const linkElement = container.querySelector('.full-line-link');
            
            const title = titleElement ? titleElement.textContent : 'Sin título';
            const meta = metaElement ? metaElement.textContent : '';
            const description = linkElement ? linkElement.getAttribute('data-description') || '' : '';
            
            const metaParts = meta.split(' | ');
            const date = metaParts[0] || '';
            const source = metaParts[1] || '';
            
            setTimeout(() => {
                // Limpiar la descripción preservando estructura de párrafos
                let cleanDescription = description || 'Content not available. Double click on the line to view the complete article.';
                
                // Convertir elementos de bloque a saltos de línea antes de limpiar
                cleanDescription = cleanDescription.replace(/<\/p>/gi, '\n\n');
                cleanDescription = cleanDescription.replace(/<br\s*\/?>/gi, '\n');
                cleanDescription = cleanDescription.replace(/<\/div>/gi, '\n');
                cleanDescription = cleanDescription.replace(/<\/h[1-6]>/gi, '\n\n');
                
                // Limpiar etiquetas HTML y entidades
                cleanDescription = cleanDescription.replace(/<[^>]*>/g, '');
                cleanDescription = cleanDescription.replace(/&[^;]+;/g, ' ');
                
                // Limpiar saltos de línea excesivos pero mantener párrafos
                cleanDescription = cleanDescription.replace(/\n\s*\n\s*\n/g, '\n\n');
                cleanDescription = cleanDescription.trim();
                
                // Convertir saltos de línea a HTML para mostrar correctamente
                if (cleanDescription.includes('\n\n')) {
                    // Si hay párrafos dobles, tratarlos como párrafos separados
                    const paragraphs = cleanDescription.split('\n\n').filter(p => p.trim());
                    cleanDescription = paragraphs.map(p => '<p>' + p.replace(/\n/g, '<br>') + '</p>').join('');
                } else {
                    // Si no hay párrafos dobles, solo reemplazar saltos de línea simples
                    cleanDescription = '<p>' + cleanDescription.replace(/\n/g, '<br>') + '</p>';
                }
                
                // Determinar posición de botones según configuración
                const buttonsPosition = localStorage.getItem('buttonsPosition') || 'right';
                
                // Botones con posición configurable
                const buttonStyle = buttonsPosition === 'right' ? 
                    'position: absolute; right: 15px; top: 15px; text-align: right;' :
                    'position: absolute; left: 15px; top: 15px; text-align: left;';
                
                const buttonsHTML = '<div style="' + buttonStyle + '">' +
                    '<div onclick="toggleSavedByLink(\'' + link + '\')" style="color: #FFD700; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">[SAVED]</div>' +
                    '<div onclick="toggleLovedByLink(\'' + link + '\')" style="color: #FF69B4; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">[FAVORITE]</div>' +
                    '<div onclick="shareArticleByLink(\'' + link + '\')" style="color: #00ff00; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px;">[SHARE]</div>' +
                    '</div>';
                
                // NO mostrar imágenes en guardados/favoritos para evitar imágenes rotas
                // Las imágenes solo están disponibles en la vista principal de feeds
                let imageHTML = '';
                // Solo mostrar imágenes si estamos en la vista de feeds (no en guardados/favoritos)
                // donde tenemos la descripción completa
                
                // Ajustar padding según posición de botones
                const contentPadding = buttonsPosition === 'right' ? 
                    'padding-right: 100px;' : 
                    'padding-left: 100px;';
                
                content.innerHTML = buttonsHTML +
                                  '<div style="' + contentPadding + '">' +
                                  (imageHTML || '') +
                                  '<div style="margin-bottom: 10px; color: #888; font-size: 12px;">' + date + ' | ' + source + '</div>' +
                                  '<div style="margin-bottom: 10px;"><a href="' + link + '" target="_blank" style="color: #ffff00; text-decoration: none;">' + title + '</a></div>' +
                                  '<div style="line-height: 1.6; color: #ccc; text-align: justify;">' + cleanDescription + '</div>' +
                                  '</div>';
            }, 300);
        }
        
        function toggleSavedByLink(link) {
            const articleIndex = articles.findIndex(a => a.link === link);
            if (articleIndex >= 0) {
                toggleSaved(articleIndex);
                updateSavedList();
                updateLovedList();
            }
        }
        
        function toggleLovedByLink(link) {
            const articleIndex = articles.findIndex(a => a.link === link);
            if (articleIndex >= 0) {
                toggleLoved(articleIndex);
                updateSavedList();
                updateLovedList();
            }
        }
        
        function shareArticleByLink(link) {
            if (navigator.share) {
                navigator.share({
                    title: 'Interesting Article',
                    url: link
                });
            } else {
                // Fallback: copy to clipboard
                navigator.clipboard.writeText(link).then(() => {
                    alert('Link copied to clipboard');
                });
            }
        }
        
        function toggleSaved(index, event) {
            if (event) {
                event.stopPropagation();
            }
            
            const article = articles[index];
            const button = document.getElementById('saved-btn-' + index);
            
            // Alternar estado (simulado - aquí harías fetch al servidor)
            article.isSaved = !article.isSaved;
            
            if (button) {
                button.textContent = article.isSaved ? '[SAVED]' : '[SAVE]';
                button.style.color = article.isSaved ? '#FFD700' : '#00ff00';
            }
            
            // Actualizar lista de guardados
            updateSavedList();
            
            console.log('Alternado guardado para:', article.title);
        }
        
        function toggleLoved(index) {
            const article = articles[index];
            const button = document.getElementById('loved-btn-' + index);
            
            // Alternar estado (simulado - aquí harías fetch al servidor)
            article.isLoved = !article.isLoved;
            
            if (button) {
                button.textContent = article.isLoved ? '[LOVED]' : '[LOVE]';
                button.style.color = article.isLoved ? '#FF69B4' : '#00ff00';
            }
            
            // Actualizar lista de favoritos
            updateLovedList();
            
            console.log('Alternado favorito para:', article.title);
        }
        
        function updateLovedList() {
            const lovedList = document.getElementById('loved-articles-list');
            const lovedArticles = articles.filter(article => article.isLoved);
            
            // Actualizar contador en la pestaña
            const lovedCount = document.getElementById('loved-count');
            if (lovedCount) {
                lovedCount.textContent = lovedArticles.length > 0 ? '(' + lovedArticles.length + ')' : '';
            }
            
            if (lovedArticles.length === 0) {
                lovedList.innerHTML = '<p style="color: #888;">No favorite articles yet.</p>';
            } else {
                let listHTML = '';
                lovedArticles.forEach((article, index) => {
                    const contentId = 'content-loved-' + btoa(article.link).replace(/=/g, '').substring(0, 10);
                    // Limpiar completamente la descripción para evitar problemas visuales
                    let cleanDescription = (article.description || '');
                    // Remover todas las etiquetas HTML incluyendo imágenes
                    cleanDescription = cleanDescription.replace(/<[^>]*>/g, '');
                    // Remover entidades HTML 
                    cleanDescription = cleanDescription.replace(/&[^;]+;/g, ' ');
                    // Remover caracteres especiales y texto truncado
                    cleanDescription = cleanDescription.replace(/\[\.\.\.\"?>.*$/g, '');
                    cleanDescription = cleanDescription.replace(/Comments.*$/g, '');
                    // Remover caracteres de control y problemáticos
                    cleanDescription = cleanDescription.replace(/[\x00-\x1F\x7F-\x9F]/g, '');
                    // Remover caracteres especiales que causan problemas visuales
                    cleanDescription = cleanDescription.replace(/[^\w\s\.\,\!\?\:\;\-\(\)\'\"\u00C0-\u00FF]/g, ' ');
                    // Limpiar espacios múltiples
                    cleanDescription = cleanDescription.replace(/\s+/g, ' ').trim();
                    // Escapar comillas para HTML
                    cleanDescription = cleanDescription.replace(/"/g, '&quot;').replace(/'/g, '&#39;');
                    
                    listHTML += '<div class="article-container">' +
                               '<div class="article-line">' +
                               '<span class="full-line-link" data-link="' + article.link + '" data-description="' + cleanDescription + '">' +
                               '<span class="meta">' + article.date + ' | ' + article.source + ' | </span>' +
                               '<span class="title">' + article.title + '</span>' +
                               '</span>' +
                               '</div>' +
                               '<div id="' + contentId + '" class="article-content"></div>' +
                               '</div>';
                });
                lovedList.innerHTML = listHTML;
            }
        }
        
        function updateSavedList() {
            const savedList = document.getElementById('saved-articles-list');
            const savedArticles = articles.filter(article => article.isSaved);
            
            // Actualizar contador en la pestaña
            const savedCount = document.getElementById('saved-count');
            if (savedCount) {
                savedCount.textContent = savedArticles.length > 0 ? '(' + savedArticles.length + ')' : '';
            }
            
            if (savedArticles.length === 0) {
                savedList.innerHTML = '<p style="color: #888;">No saved articles yet.</p>';
            } else {
                let listHTML = '';
                savedArticles.forEach((article, index) => {
                    const contentId = 'content-saved-' + btoa(article.link).replace(/=/g, '').substring(0, 10);
                    // Limpiar completamente la descripción para evitar problemas visuales
                    let cleanDescription = (article.description || '');
                    // Remover todas las etiquetas HTML incluyendo imágenes
                    cleanDescription = cleanDescription.replace(/<[^>]*>/g, '');
                    // Remover entidades HTML 
                    cleanDescription = cleanDescription.replace(/&[^;]+;/g, ' ');
                    // Remover caracteres especiales y texto truncado
                    cleanDescription = cleanDescription.replace(/\[\.\.\.\"?>.*$/g, '');
                    cleanDescription = cleanDescription.replace(/Comments.*$/g, '');
                    // Remover caracteres de control y problemáticos
                    cleanDescription = cleanDescription.replace(/[\x00-\x1F\x7F-\x9F]/g, '');
                    // Remover caracteres especiales que causan problemas visuales
                    cleanDescription = cleanDescription.replace(/[^\w\s\.\,\!\?\:\;\-\(\)\'\"\u00C0-\u00FF]/g, ' ');
                    // Limpiar espacios múltiples
                    cleanDescription = cleanDescription.replace(/\s+/g, ' ').trim();
                    // Escapar comillas para HTML
                    cleanDescription = cleanDescription.replace(/"/g, '&quot;').replace(/'/g, '&#39;');
                    
                    listHTML += '<div class="article-container">' +
                               '<div class="article-line">' +
                               '<span class="full-line-link" data-link="' + article.link + '" data-description="' + cleanDescription + '">' +
                               '<span class="meta">' + article.date + ' | ' + article.source + ' | </span>' +
                               '<span class="title">' + article.title + '</span>' +
                               '</span>' +
                               '</div>' +
                               '<div id="' + contentId + '" class="article-content"></div>' +
                               '</div>';
                });
                savedList.innerHTML = listHTML;
            }
        }
        
        function removeLoved(articleLink) {
            // Encontrar y quitar de favoritos
            articles.forEach(article => {
                if (article.link === articleLink) {
                    article.isLoved = false;
                    // Actualizar botón si existe
                    const button = document.getElementById('loved-btn-' + articles.indexOf(article));
                    if (button) {
                        button.textContent = '[MARCAR FAV]';
                        button.style.color = '#00ff00';
                    }
                }
            });
            updateLovedList();
        }
        
        function removeSaved(articleLink) {
            // Encontrar y quitar de guardados
            articles.forEach(article => {
                if (article.link === articleLink) {
                    article.isSaved = false;
                    // Actualizar botón si existe
                    const button = document.getElementById('saved-btn-' + articles.indexOf(article));
                    if (button) {
                        button.textContent = '[GUARDAR]';
                        button.style.color = '#00ff00';
                    }
                }
            });
            updateSavedList();
        }
        
        function shareArticle(index) {
            const article = articles[index];
            
            if (navigator.share) {
                navigator.share({
                    title: article.title,
                    text: article.title,
                    url: article.link
                });
            } else {
                // Fallback: copy to clipboard
                navigator.clipboard.writeText(article.link).then(() => {
                    alert('Link copied to clipboard');
                });
            }
        }
        
        function changeButtonsPosition() {
            const select = document.getElementById('buttonsConfig');
            const modalSelect = document.getElementById('buttonsConfigModal');
            
            let newValue;
            if (select && select.value) {
                newValue = select.value;
                if (modalSelect) modalSelect.value = newValue;
            } else if (modalSelect && modalSelect.value) {
                newValue = modalSelect.value;
                if (select) select.value = newValue;
            }
            
            if (newValue) {
                localStorage.setItem('buttonsPosition', newValue);
                
                // Recargar contenido de artículos expandidos
                document.querySelectorAll('.article-content.expanded').forEach((content, index) => {
                    const articleIndex = parseInt(content.id.replace('content-', ''));
                    loadArticleContent(articleIndex);
                });
            }
        }
        
        function showPage(pageName, event) {
            console.log('showPage llamada con:', pageName, event);
            if (event) {
                event.preventDefault();
            }
            
            // Actualizar la página actual
            currentPage = pageName;
            
            // Ocultar todos los contenidos
            const contentIds = ['feeds-content', 'import-content', 'saved-content', 'loved-content', 'config-content'];
            contentIds.forEach(id => {
                const element = document.getElementById(id);
                console.log('Elemento', id, ':', element);
                if (element) element.style.display = 'none';
            });
            
            // Quitar clase activa de todas las pestañas
            document.querySelectorAll('.tab').forEach(tab => {
                tab.classList.remove('tab-active');
            });
            
            // Mostrar el contenido solicitado
            const targetContent = document.getElementById(pageName + '-content');
            console.log('Target content para', pageName + '-content', ':', targetContent);
            if (targetContent) {
                targetContent.style.display = 'block';
                console.log('Contenido mostrado para:', pageName);
            } else {
                console.error('No se encontró el contenido para:', pageName + '-content');
            }
            
            // Activar la pestaña correspondiente
            if (event && event.target) {
                event.target.classList.add('tab-active');
            } else {
                // Si no hay event.target, buscar la pestaña por el pageName
                const tabs = document.querySelectorAll('.tab');
                tabs.forEach(tab => {
                    if (tab.textContent.toLowerCase().includes(pageName.toLowerCase())) {
                        tab.classList.add('tab-active');
                        console.log('Tab activada:', tab.textContent);
                    }
                });
            }
            
            // Actualizar título
            document.title = 'ANCAP WEB - ' + pageName.toUpperCase();
            
            // Actualizar listas y reiniciar selección
            if (pageName === 'saved') {
                updateSavedList();
                selectedIndex = 0;
                setTimeout(highlightSelected, 100);
            } else if (pageName === 'loved') {
                updateLovedList();
                selectedIndex = 0;
                setTimeout(highlightSelected, 100);
            } else if (pageName === 'feeds') {
                selectedIndex = 0;
                setTimeout(highlightSelected, 100);
            } else {
                selectedIndex = -1; // Para config u otras páginas sin artículos
            }
        }
        
        function closeAllModals() {
            // Esta función ya no es necesaria para páginas, pero la mantenemos por compatibilidad
            document.title = 'ANCAP WEB';
        }
        
        function closeModal(windowName) {
            // Ya no es necesaria, pero la mantenemos por compatibilidad
            showPage('feeds');
        }
        
        document.addEventListener('DOMContentLoaded', function() {
            // Función para actualizar fecha y hora
            function updateDateTime() {
                const now = new Date();
                const dateOptions = { 
                    weekday: 'short', 
                    year: 'numeric', 
                    month: 'short', 
                    day: 'numeric' 
                };
                const timeOptions = { 
                    hour: '2-digit', 
                    minute: '2-digit', 
                    second: '2-digit',
                    hour12: false
                };
                
                const dateElement = document.getElementById('current-date');
                const timeElement = document.getElementById('current-time');
                
                if (dateElement && timeElement) {
                    dateElement.textContent = now.toLocaleDateString('es-ES', dateOptions);
                    timeElement.textContent = now.toLocaleTimeString('es-ES', timeOptions);
                }
            }
            
            // Actualizar inmediatamente
            updateDateTime();
            
            // Actualizar cada segundo
            setInterval(updateDateTime, 1000);
            
            // Recopilar información de artículos
            document.querySelectorAll('.article-container').forEach((container, index) => {
                const span = container.querySelector('.full-line-link');
                const meta = container.querySelector('.meta').textContent;
                const title = container.querySelector('.title').textContent;
                
                const parts = meta.split(' | ');
                articles.push({
                    title: title,
                    date: parts[0] || '',
                    source: parts[1] || '',
                    link: span.getAttribute('data-link'),
                    description: span.getAttribute('data-description') || '',
                    isRead: false,
                    isSaved: false,
                    isLoved: false
                });
            });
            
            // Actualizar contador de feeds
            const feedsCount = document.getElementById('feeds-count');
            if (feedsCount) {
                feedsCount.textContent = articles.length > 0 ? '(' + articles.length + ')' : '';
            }
            
            // Inicializar selección en el primer artículo
            if (articles.length > 0) {
                selectedIndex = 0;
                highlightSelected();
            }
            
            // Cargar configuración de posición de botones
            const savedPosition = localStorage.getItem('buttonsPosition') || 'right';
            const buttonsSelect = document.getElementById('buttonsConfig');
            const buttonsModalSelect = document.getElementById('buttonsConfigModal');
            if (buttonsSelect) {
                buttonsSelect.value = savedPosition;
            }
            if (buttonsModalSelect) {
                buttonsModalSelect.value = savedPosition;
            }
        });
    </script>
</head>
<body>
    <div class="container">
        <div class="main-header">
            <div class="datetime-display">
                <div id="current-date"></div>
                <div id="current-time"></div>
            </div>
            <div class="ascii-logo">
▄▀█ █▄ █ █▀▀ ▄▀█ █▀▄
█▀█ █ ▀█ █▄▄ █▀█ █▀▀
            </div>
            <div class="subtitle">» A LIBERTARIAN RSS READER «</div>
            <div style="position: absolute; top: 8px; right: 10px;">
                <a href="/logout" style="color: #888; text-decoration: none; font-size: 10px;">[LOGOUT]</a>
            </div>
        </div>
        <div class="tabs">
            <a class="tab tab-active" href="#" onclick="showPage('feeds', event)">FEEDS <span id="feeds-count"></span> <span class="tab-shortcut">(F1)</span></a>
            <a class="tab" href="#" onclick="showPage('saved', event)">SAVED <span id="saved-count"></span> <span class="tab-shortcut">(F2)</span></a>
            <a class="tab" href="#" onclick="showPage('loved', event)">LOVED <span id="loved-count"></span> <span class="tab-shortcut">(F3)</span></a>
            <a class="tab" href="#" onclick="showPage('config', event)">CONFIG <span class="tab-shortcut">(F4)</span></a>
        </div>
        
        <!-- Contenido principal de feeds -->
        <div id="feeds-content" class="page-content" style="display: block;">
            <div style="height: 1px; background: #000; margin-bottom: 0px;"></div>`

	for i, article := range data.Articles {
		if i >= 50 {
			break
		}
		// Limpiar y escapar caracteres especiales para HTML/JavaScript
		description := article.Description

		// Limpiar HTML tags y contenido problemático antes de escapar
		description = strings.ReplaceAll(description, "&lt;", "<")
		description = strings.ReplaceAll(description, "&gt;", ">")
		description = strings.ReplaceAll(description, "&amp;", "&")

		// Remover contenido problemático común en feeds
		description = strings.ReplaceAll(description, "Comments\"", "")
		description = strings.ReplaceAll(description, "[...]\"", "")

		// Escapar caracteres especiales para HTML
		description = strings.ReplaceAll(description, "&", "&amp;")
		description = strings.ReplaceAll(description, "<", "&lt;")
		description = strings.ReplaceAll(description, ">", "&gt;")
		description = strings.ReplaceAll(description, `"`, "&quot;")
		description = strings.ReplaceAll(description, `'`, "&#39;")
		description = strings.ReplaceAll(description, "\n", " ")
		description = strings.ReplaceAll(description, "\r", " ")
		description = strings.ReplaceAll(description, "\t", " ")

		// Limpiar título también
		title := article.Title
		title = strings.ReplaceAll(title, "&", "&amp;")
		title = strings.ReplaceAll(title, "<", "&lt;")
		title = strings.ReplaceAll(title, ">", "&gt;")
		title = strings.ReplaceAll(title, `"`, "&quot;")
		title = strings.ReplaceAll(title, `'`, "&#39;")

		// Limitar longitud para evitar atributos muy largos, pero permitir más espacio para imágenes
		if len(description) > 2000 {
			description = description[:2000] + "..."
		}

		html += `<div class="article-container">
            <div class="article-line" onclick="toggleArticle(` + fmt.Sprintf("%d", i) + `, event)">
                <span class="full-line-link" data-link="` + article.Link + `" data-description="` + description + `">
                    <span class="meta">` + article.Date + ` | <span style="color: #ffff00;">` + article.Source + `</span> | </span>
                    <span class="title">` + title + `</span>
                </span>
            </div>
            <div id="content-` + fmt.Sprintf("%d", i) + `" class="article-content"></div>
        </div>`
	}

	html += `        </div>
        
        <!-- Saved Articles Page -->
        <div id="saved-content" class="page-content" style="display: none;">
            <div class="page-header">SAVED ARTICLES (F2)</div>
            <p>List of articles marked as saved.</p>
            <div id="saved-articles-list">
                <p style="color: #888;">No saved articles yet.</p>
            </div>
        </div>
        
        <!-- Loved Page -->
        <div id="loved-content" class="page-content" style="display: none;">
            <div class="page-header">LOVED ARTICLES (F3)</div>
            <p>List of articles marked as loved.</p>
            <div id="loved-articles-list">
                <p style="color: #888;">No favorite articles yet.</p>
            </div>
        </div>
        
        <!-- Config Page -->
        <div id="config-content" class="page-content" style="display: none;">
            <div class="page-header">CONFIGURATION (F4)</div>
            
            <div class="config-section">
                <h3>RSS Feeds</h3>
                <label class="config-label">RSS Feed URL:</label>
                <input type="text" class="config-input" placeholder="https://example.com/feed.xml">
                <button onclick="alert('Feature to be implemented')" style="background: none; border: 1px solid #00ff00; color: #00ff00; padding: 3px 8px; font-family: 'JetBrains Mono', monospace; font-size: 12px; cursor: pointer; margin-left: 10px;">Add</button>
                
                <br><br>
                <label class="config-label">Import OPML file:</label>
                <input type="file" class="config-input" accept=".opml,.xml">
                <button onclick="alert('Feature to be implemented')" style="background: none; border: 1px solid #00ff00; color: #00ff00; padding: 3px 8px; font-family: 'JetBrains Mono', monospace; font-size: 12px; cursor: pointer; margin-left: 10px;">Import</button>
            </div>
            
            <div class="config-section">
                <h3>Interface</h3>
                <label class="config-label">Article buttons position:</label>
                <select id="buttonsConfigModal" onchange="changeButtonsPosition()" class="config-select">
                    <option value="right">Right</option>
                    <option value="left">Left</option>
                </select>
            </div>
            
            <div class="config-section">
                <h3>Feeds</h3>
                <label class="config-label">Update interval (minutes):</label>
                <input type="number" class="config-input" value="30" min="5" max="1440">
            </div>
            
            <div class="config-section">
                <h3>Appearance</h3>
                <label class="config-label">Theme:</label>
                <select class="config-select">
                    <option value="green">Green (current)</option>
                    <option value="blue">Blue</option>
                    <option value="amber">Amber</option>
                </select>
            </div>
            
            <div class="config-section">
                <h3>Keyboard shortcuts</h3>
                <p><strong>F1-F4:</strong> Switch between pages</p>
                <p><strong>↑/↓, J/K:</strong> Navigate articles</p>
                <p><strong>Space/Enter:</strong> Expand article</p>
                <p><strong>S/L/C:</strong> Save/Favorite/Share</p>
                <p><strong>Escape:</strong> Return to FEEDS</p>
            </div>
        </div>
    </div>
</body>
</html>`

	w.Write([]byte(html))
	log.Printf("✅ HTML rendered successfully with %d articles", len(data.Articles))
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	feeds := loadFeeds()
	var allArticles []Article
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, feed := range feeds {
		if !feed.Active {
			continue
		}
		wg.Add(1)
		go func(feedURL string) {
			defer wg.Done()
			articles := getCachedOrFetch(feedURL)
			mu.Lock()
			allArticles = append(allArticles, articles...)
			mu.Unlock()
		}(feed.URL)
	}
	wg.Wait()

	for i := range allArticles {
		allArticles[i].IsFav = isArticleFavorite(allArticles[i].Link)
	}

	// Sort articles by date and title to ensure consistent order
	sort.Slice(allArticles, func(i, j int) bool {
		if allArticles[i].Date == allArticles[j].Date {
			return allArticles[i].Title < allArticles[j].Title
		}
		return allArticles[i].Date > allArticles[j].Date
	})

	if len(allArticles) > 50 {
		allArticles = allArticles[:50]
	}

	// Precargar contenido completo de los primeros artículos en segundo plano
	go preloadArticleContent(allArticles)

	data := TemplateData{
		Articles: allArticles,
	}

	elapsed := time.Since(startTime)
	log.Printf("⚡ Home handler completed in %v with %d articles (CACHE + PARALLEL)", elapsed, len(allArticles))
	renderHomePage(w, data)
}

func preloadArticleContent(articles []Article) {
	log.Printf("🔄 Iniciando precarga de contenido para %d artículos", len(articles))

	// Limitar a los primeros 10 artículos para no sobrecargar
	maxArticles := 10
	if len(articles) < maxArticles {
		maxArticles = len(articles)
	}

	var wg sync.WaitGroup
	for i := 0; i < maxArticles; i++ {
		article := articles[i]

		// Verificar si ya está en cache
		articleContentCache.mutex.RLock()
		cached, exists := articleContentCache.articles[article.Link]
		articleContentCache.mutex.RUnlock()

		// Solo precargar si no existe en cache o es muy antiguo
		if !exists || time.Since(cached.Timestamp) > 30*time.Minute {
			wg.Add(1)
			go func(url string, title string) {
				defer wg.Done()

				content, err := scrapeArticleContent(url)
				success := err == nil

				// Si el scraping falla, usar contenido vacío pero marcar como intentado
				if !success {
					content = ""
				}

				// Guardar en cache
				articleContentCache.mutex.Lock()
				articleContentCache.articles[url] = CachedArticleContent{
					Content:   content,
					Timestamp: time.Now(),
					Success:   success,
				}
				articleContentCache.mutex.Unlock()

				if success {
					log.Printf("✅ Precargado: %s", title)
				} else {
					log.Printf("⚠️ Falló precarga: %s", title)
				}
			}(article.Link, article.Title)
		}
	}
	wg.Wait()
	log.Printf("🎯 Precarga de contenido completada")
}

func fetchFeedArticles(feedURL string) []Article {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}

	fp := gofeed.NewParser()
	fp.Client = client

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Printf("🌐 Intentando acceder al feed: %s", feedURL)
	feed, err := fp.ParseURLWithContext(feedURL, ctx)
	if err != nil {
		log.Printf("❌ Error al acceder al feed %s: %v", feedURL, err)
		return []Article{}
	}

	log.Printf("✅ Feed obtenido exitosamente: %s", feed.Title)

	var articles []Article
	for _, item := range feed.Items {
		date := ""
		if item.PublishedParsed != nil {
			date = item.PublishedParsed.Format("2006-01-02 15:04")
		} else if item.Published != "" {
			date = item.Published
		}

		description := ""
		if item.Description != "" {
			description = item.Description
		} else if item.Content != "" {
			description = item.Content
		}

		article := Article{
			Title:       item.Title,
			Link:        item.Link,
			Date:        date,
			Source:      feed.Title,
			Description: description,
		}
		articles = append(articles, article)
	}

	return articles
}

func addHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("➕ Add handler called")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	feedURL := r.FormValue("url")
	if feedURL == "" {
		http.Error(w, "URL required", http.StatusBadRequest)
		return
	}

	feed := Feed{URL: feedURL, Active: true}
	if err := saveFeed(feed); err != nil {
		log.Printf("❌ Error saving feed: %v", err)
		http.Error(w, "Error saving feed", http.StatusInternalServerError)
		return
	}

	log.Printf("✅ Feed added: %s", feedURL)
	w.Write([]byte("Feed added successfully"))
}

func favoriteHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("⭐ Favorite handler called")

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	title := r.FormValue("title")
	link := r.FormValue("link")
	date := r.FormValue("date")
	source := r.FormValue("source")

	if title == "" || link == "" {
		log.Printf("❌ Missing required favorite data")
		http.Error(w, "Title and link required", http.StatusBadRequest)
		return
	}

	favorite := FavoriteArticle{
		Title:  title,
		Link:   link,
		Date:   date,
		Source: source,
	}

	favorites := loadFavoriteArticles()

	// Verificar si ya existe
	exists := false
	for _, fav := range favorites {
		if fav.Link == link {
			exists = true
			break
		}
	}

	if !exists {
		favorites = append(favorites, favorite)

		favoritesJSON, err := json.MarshalIndent(favorites, "", "  ")
		if err != nil {
			log.Printf("❌ Error marshaling favorites: %v", err)
			http.Error(w, "Error saving favorite", http.StatusInternalServerError)
			return
		}

		if err := os.WriteFile("favorites.json", favoritesJSON, 0644); err != nil {
			log.Printf("❌ Error saving favorites file: %v", err)
			http.Error(w, "Error saving favorite", http.StatusInternalServerError)
			return
		}

		log.Printf("✅ Favorite added: %s", title)
		w.Write([]byte("Added"))
	} else {
		log.Printf("ℹ️ Favorite already exists: %s", title)
		w.Write([]byte("Already exists"))
	}
}

func apiFavoritesHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("📋 API favorites handler called")

	favorites := loadFavoriteArticles()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(favorites); err != nil {
		log.Printf("❌ Error encoding favorites: %v", err)
		http.Error(w, "Error loading favorites", http.StatusInternalServerError)
		return
	}

	log.Printf("✅ Returned %d favorites", len(favorites))
}

func clearCacheHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("🧹 Clear cache handler called")
	globalCache.mutex.Lock()
	globalCache.feeds = make(map[string]CachedFeed)
	globalCache.mutex.Unlock()
	log.Printf("✅ Cache cleared successfully")
	w.Write([]byte("Cache cleared successfully"))
}

func scrapeArticleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	var request struct {
		URL string `json:"url"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		log.Printf("❌ Error decoding scrape request: %v", err)
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	log.Printf("🔍 Attempting to scrape article: %s", request.URL)

	// Verificar primero si está en cache
	articleContentCache.mutex.RLock()
	cached, exists := articleContentCache.articles[request.URL]
	articleContentCache.mutex.RUnlock()

	var content string
	var err error

	if exists && cached.Success && time.Since(cached.Timestamp) < 30*time.Minute {
		// Usar contenido del cache
		content = cached.Content
		log.Printf("🟢 Cache HIT para artículo: %s", request.URL)
	} else {
		// Hacer scraping y guardar en cache
		content, err = scrapeArticleContent(request.URL)
		if err != nil {
			log.Printf("❌ Error scraping article: %v", err)
			http.Error(w, "Could not scrape article", http.StatusInternalServerError)
			return
		}

		// Guardar en cache
		articleContentCache.mutex.Lock()
		articleContentCache.articles[request.URL] = CachedArticleContent{
			Content:   content,
			Timestamp: time.Now(),
			Success:   true,
		}
		articleContentCache.mutex.Unlock()
		log.Printf("🔴 Cache MISS - scraped y guardado: %s", request.URL)
	}

	response := struct {
		Content string `json:"content"`
		URL     string `json:"url"`
	}{
		Content: content,
		URL:     request.URL,
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("❌ Error encoding scrape response: %v", err)
		http.Error(w, "Error encoding response", http.StatusInternalServerError)
		return
	}

	log.Printf("✅ Successfully scraped article content (%d characters)", len(content))
}

func scrapeArticleContent(url string) (string, error) {
	// Crear cliente HTTP con timeout
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	// Hacer request a la URL del artículo
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("error fetching URL: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("non-200 status code: %d", resp.StatusCode)
	}

	// Leer el contenido HTML
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response body: %v", err)
	}

	// Convertir a string
	html := string(body)

	// Extraer el contenido principal usando selectores comunes
	content := extractMainContent(html)

	// Limpiar y formatear el contenido
	content = cleanScrapedContent(content)

	// Ser más tolerante con el contenido corto
	if len(content) < 50 {
		// Intentar extraer al menos el título y algo de contenido
		titleContent := extractTitleAndMeta(html)
		if len(titleContent) > 20 {
			content = titleContent
		} else {
			return "", fmt.Errorf("extracted content too short (%d chars), probably failed", len(content))
		}
	}

	return content, nil
}

func extractMainContent(html string) string {
	// Selectores comunes para contenido principal
	contentSelectors := []string{
		`<article[^>]*>([\s\S]*?)</article>`,
		`<div[^>]*class[^>]*["'].*?content.*?["'][^>]*>([\s\S]*?)</div>`,
		`<div[^>]*class[^>]*["'].*?post.*?["'][^>]*>([\s\S]*?)</div>`,
		`<div[^>]*class[^>]*["'].*?entry.*?["'][^>]*>([\s\S]*?)</div>`,
		`<div[^>]*class[^>]*["'].*?main.*?["'][^>]*>([\s\S]*?)</div>`,
		`<main[^>]*>([\s\S]*?)</main>`,
	}

	// Intentar cada selector
	for _, selector := range contentSelectors {
		re := regexp.MustCompile(selector)
		matches := re.FindStringSubmatch(html)
		if len(matches) > 1 && len(matches[1]) > 100 {
			return matches[1]
		}
	}

	// Fallback: extraer entre <body> tags
	bodyRe := regexp.MustCompile(`<body[^>]*>([\s\S]*?)</body>`)
	matches := bodyRe.FindStringSubmatch(html)
	if len(matches) > 1 {
		return matches[1]
	}

	return html
}

func extractTitleAndMeta(html string) string {
	// Fallback: extraer al menos título y meta descripción
	var content strings.Builder

	// Extraer título
	titleRe := regexp.MustCompile(`<title[^>]*>([^<]+)</title>`)
	if matches := titleRe.FindStringSubmatch(html); len(matches) > 1 {
		content.WriteString("# ")
		content.WriteString(strings.TrimSpace(matches[1]))
		content.WriteString("\n\n")
	}

	// Extraer meta descripción
	metaRe := regexp.MustCompile(`<meta[^>]*name=["\']description["\'][^>]*content=["\']([^"\']+)["\']`)
	if matches := metaRe.FindStringSubmatch(html); len(matches) > 1 {
		content.WriteString(strings.TrimSpace(matches[1]))
		content.WriteString("\n\n")
	}

	// Extraer algunos párrafos del contenido
	pRe := regexp.MustCompile(`<p[^>]*>([^<]{20,200})</p>`)
	matches := pRe.FindAllStringSubmatch(html, 3)
	for _, match := range matches {
		if len(match) > 1 {
			cleanP := strings.TrimSpace(match[1])
			if len(cleanP) > 20 {
				content.WriteString(cleanP)
				content.WriteString("\n\n")
			}
		}
	}

	return content.String()
}

func cleanScrapedContent(content string) string {
	// Remover scripts y styles
	scriptRe := regexp.MustCompile(`<script[^>]*>[\s\S]*?</script>`)
	content = scriptRe.ReplaceAllString(content, "")

	styleRe := regexp.MustCompile(`<style[^>]*>[\s\S]*?</style>`)
	content = styleRe.ReplaceAllString(content, "")

	// Remover comentarios HTML
	commentRe := regexp.MustCompile(`<!--[\s\S]*?-->`)
	content = commentRe.ReplaceAllString(content, "")

	// Remover navegación y elementos comunes no deseados
	unwantedSelectors := []string{
		`<nav[^>]*>[\s\S]*?</nav>`,
		`<header[^>]*>[\s\S]*?</header>`,
		`<footer[^>]*>[\s\S]*?</footer>`,
		`<div[^>]*class[^>]*["'].*?nav.*?["'][^>]*>[\s\S]*?</div>`,
		`<div[^>]*class[^>]*["'].*?menu.*?["'][^>]*>[\s\S]*?</div>`,
		`<div[^>]*class[^>]*["'].*?sidebar.*?["'][^>]*>[\s\S]*?</div>`,
		`<div[^>]*class[^>]*["'].*?advertisement.*?["'][^>]*>[\s\S]*?</div>`,
		`<div[^>]*class[^>]*["'].*?ads.*?["'][^>]*>[\s\S]*?</div>`,
	}

	for _, selector := range unwantedSelectors {
		re := regexp.MustCompile(selector)
		content = re.ReplaceAllString(content, "")
	}

	// Convertir algunas etiquetas HTML a texto plano
	content = strings.ReplaceAll(content, "<br>", "\n")
	content = strings.ReplaceAll(content, "<br/>", "\n")
	content = strings.ReplaceAll(content, "<br />", "\n")
	content = strings.ReplaceAll(content, "</p>", "\n\n")
	content = strings.ReplaceAll(content, "</div>", "\n")
	content = strings.ReplaceAll(content, "</h1>", "\n\n")
	content = strings.ReplaceAll(content, "</h2>", "\n\n")
	content = strings.ReplaceAll(content, "</h3>", "\n\n")
	content = strings.ReplaceAll(content, "</h4>", "\n\n")
	content = strings.ReplaceAll(content, "</h5>", "\n\n")
	content = strings.ReplaceAll(content, "</h6>", "\n\n")

	// Remover todas las etiquetas HTML restantes
	htmlTagRe := regexp.MustCompile(`<[^>]*>`)
	content = htmlTagRe.ReplaceAllString(content, "")

	// Decodificar entidades HTML comunes
	content = strings.ReplaceAll(content, "&amp;", "&")
	content = strings.ReplaceAll(content, "&lt;", "<")
	content = strings.ReplaceAll(content, "&gt;", ">")
	content = strings.ReplaceAll(content, "&quot;", "\"")
	content = strings.ReplaceAll(content, "&#39;", "'")
	content = strings.ReplaceAll(content, "&nbsp;", " ")

	// Limpiar espacios en blanco excesivos
	spaceRe := regexp.MustCompile(`\s+`)
	content = spaceRe.ReplaceAllString(content, " ")

	// Limpiar saltos de línea excesivos
	newlineRe := regexp.MustCompile(`\n\s*\n\s*\n`)
	content = newlineRe.ReplaceAllString(content, "\n\n")

	return strings.TrimSpace(content)
}

func staticHandler(w http.ResponseWriter, r *http.Request) {
	// Servir archivos estáticos
	http.ServeFile(w, r, "."+r.URL.Path)
}

func preloadFeedsHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("🔄 Preload feeds handler called")

	// Solo permitir GET
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Cargar feeds en segundo plano y llenar cache
	feeds := loadFeeds()
	var wg sync.WaitGroup

	for _, feed := range feeds {
		wg.Add(1)
		go func(feedURL string) {
			defer wg.Done()
			// Esto llenará el cache
			fetchFeedArticles(feedURL)
		}(feed.URL)
	}

	wg.Wait()

	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"status":      "success",
		"message":     "Feeds precargados en cache",
		"feeds_count": len(feeds),
	}

	json.NewEncoder(w).Encode(response)
	log.Printf("✅ Feeds precargados exitosamente (%d feeds)", len(feeds))
}

func loginPageHandler(w http.ResponseWriter, r *http.Request) {
	// Verificar si ya tiene sesión válida
	cookie, err := r.Cookie("session_id")
	if err == nil {
		if _, valid := validateSession(cookie.Value); valid {
			http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
			return
		}
	}

	// Renderizar página de login (usar la misma lógica que home pero solo login)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Solo mostrar la pantalla de login
	html := `<!DOCTYPE html>
<html lang="es">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>ANCAP WEB - LOGIN</title>
    <style>
        @font-face {
            font-family: 'JetBrains Mono';
            src: url('/static/fonts/JetBrainsMonoNerdFont-Regular.woff2') format('woff2'),
                 url('/static/fonts/JetBrainsMono/JetBrainsMonoNerdFont-Regular.ttf') format('truetype');
            font-weight: 400;
            font-style: normal;
            font-display: swap;
        }
        body { 
            background: #000; 
            color: #00ff00; 
            font-family: 'JetBrains Mono', 'Courier New', monospace; 
            margin: 0; 
            padding: 20px;
            height: 100vh;
            display: flex;
            justify-content: center;
            align-items: center;
            background-image: 
                radial-gradient(rgba(0, 255, 0, 0.03) 1px, transparent 1px);
            background-size: 20px 20px;
            padding-top: 2vh;
        }
        
        .login-container {
            text-align: center;
            max-width: 400px;
            width: 100%;
            display: flex;
            flex-direction: column;
            align-items: center;
        }
        
        .login-brand {
            font-size: 20px;
            font-weight: bold;
            margin-bottom: 12px;
            color: #00ff00;
            text-align: center;
            font-family: 'Courier New', monospace;
            white-space: pre;
            line-height: 1.0;
            text-shadow: 0 0 20px #00ff00, 0 0 40px #00ff00;
            animation: glow 2s ease-in-out infinite alternate;
        }
        
        @keyframes glow {
            from { text-shadow: 0 0 20px #00ff00, 0 0 40px #00ff00; }
            to { text-shadow: 0 0 30px #00ff00, 0 0 60px #00ff00; }
        }
        
        .login-subtitle {
            font-size: 14px;
            color: #00aa00;
            margin-top: 5px;
            margin-bottom: 35px;
            letter-spacing: 2px;
            text-transform: uppercase;
        }
        
        .login-content {
            background: transparent;
            padding: 0;
            width: 100%;
        }
        
        .login-form {
            margin-top: 0;
            width: 100%;
            display: flex;
            flex-direction: column;
            align-items: center;
        }
        
        .login-input {
            background: rgba(0, 0, 0, 0.9);
            border: 2px dashed #00ff00;
            border-radius: 12px;
            color: #00ff00;
            font-family: 'JetBrains Mono', monospace;
            font-size: 18px;
            padding: 12px 18px;
            margin: 8px 0;
            width: 100%;
            max-width: 320px;
            box-sizing: border-box;
            text-align: center;
            letter-spacing: 1px;
            transition: all 0.3s ease;
            backdrop-filter: blur(5px);
            box-shadow: 
                0 0 20px rgba(0, 255, 0, 0.2),
                inset 0 0 20px rgba(0, 255, 0, 0.05);
        }
        
        .login-input::placeholder {
            color: #006600;
            opacity: 0.8;
        }
        
        .login-input:focus {
            outline: none;
            border-color: #00ff00;
            box-shadow: 
                0 0 25px rgba(0, 255, 0, 0.6),
                inset 0 0 15px rgba(0, 255, 0, 0.1);
            transform: scale(1.03);
        }
        
        .login-button {
            background: rgba(0, 0, 0, 0.9);
            border: 2px solid #00ff00;
            border-radius: 12px;
            color: #00ff00;
            font-family: 'JetBrains Mono', monospace;
            font-size: 16px;
            font-weight: bold;
            padding: 12px 25px;
            cursor: pointer;
            margin-top: 15px;
            width: 100%;
            max-width: 320px;
            transition: all 0.3s ease;
            letter-spacing: 2px;
            text-transform: uppercase;
            backdrop-filter: blur(5px);
        }
        
        .login-button:hover {
            background: #00ff00;
            color: #000;
            box-shadow: 0 0 30px rgba(0, 255, 0, 0.7);
            transform: translateY(-2px);
        }
        
        .login-button:active {
            transform: translateY(0);
        }
        
        .login-error {
            color: #ff3333;
            margin-top: 20px;
            font-size: 14px;
            background: rgba(255, 0, 0, 0.1);
            border: 1px dashed #ff3333;
            border-radius: 10px;
            padding: 10px;
        }
        
        .login-info {
            color: #666;
            font-size: 11px;
            margin-top: 30px;
            line-height: 1.6;
            border-top: 1px dashed #333;
            padding-top: 20px;
        }
    </style>
    <script>
        function handleLogin(event) {
            event.preventDefault();
            
            const username = document.getElementById('username').value;
            const password = document.getElementById('password').value;
            const errorDiv = document.getElementById('login-error');
            
            if (!username || !password) {
                errorDiv.textContent = 'Por favor ingresa usuario y contraseña';
                errorDiv.style.display = 'block';
                return;
            }
            
            errorDiv.style.display = 'none';
            
            const button = document.getElementById('login-button');
            button.textContent = 'ACCEDIENDO...';
            button.disabled = true;
            
            fetch('/api/login', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({
                    username: username,
                    password: password
                })
            })
            .then(response => {
                if (response.ok) {
                    return response.json();
                } else {
                    throw new Error('Credenciales inválidas');
                }
            })
            .then(data => {
                if (data.success) {
                    // Si los feeds están precargados, la página cargará más rápido
                    if (feedsPreloaded) {
                        console.log('🚀 Login exitoso con feeds precargados - carga rápida');
                    } else {
                        console.log('✅ Login exitoso - cargando feeds...');
                    }
                    window.location.href = '/';
                } else {
                    throw new Error(data.message || 'Error de autenticación');
                }
            })
            .catch(error => {
                errorDiv.textContent = error.message;
                errorDiv.style.display = 'block';
                button.textContent = 'ACCEDER';
                button.disabled = false;
                document.getElementById('password').value = '';
                document.getElementById('password').focus();
            });
        }
        
        function handleKeyPress(event) {
            if (event.key === 'Enter') {
                handleLogin(event);
            }
        }
        
        // Variable para controlar la precarga
        let preloadTimer = null;
        let feedsPreloaded = false;
        
        function preloadFeeds(username) {
            if (feedsPreloaded) return;
            
            console.log('🔄 Precargando feeds para usuario:', username);
            
            // Hacer precarga de feeds usando el endpoint específico
            fetch('/api/preload-feeds', {
                method: 'GET',
                credentials: 'same-origin'
            }).then(response => {
                if (response.ok) {
                    return response.json();
                }
                throw new Error('Error en precarga');
            }).then(data => {
                console.log('✅ Feeds precargados:', data);
                feedsPreloaded = true;
                
                // Mostrar indicador visual sutil
                const usernameInput = document.getElementById('username');
                usernameInput.style.borderColor = '#00aa00';
                usernameInput.style.boxShadow = '0 0 15px rgba(0, 255, 0, 0.3)';
                
                setTimeout(() => {
                    usernameInput.style.borderColor = '#00ff00';
                    usernameInput.style.boxShadow = '0 0 20px rgba(0, 255, 0, 0.2), inset 0 0 20px rgba(0, 255, 0, 0.05)';
                }, 2000);
            }).catch(error => {
                console.log('❌ Error precargando feeds:', error);
            });
        }
        
        function handleUsernameInput(event) {
            const username = event.target.value.trim();
            
            // Cancelar timer anterior si existe
            if (preloadTimer) {
                clearTimeout(preloadTimer);
            }
            
            // Si el usuario escribió algo, esperar 800ms y precargar
            if (username.length >= 3) {
                preloadTimer = setTimeout(() => {
                    preloadFeeds(username);
                }, 800);
            }
        }
        
        document.addEventListener('DOMContentLoaded', function() {
            const usernameInput = document.getElementById('username');
            usernameInput.focus();
            
            // Agregar evento de input para detectar cuando se escribe el usuario
            usernameInput.addEventListener('input', handleUsernameInput);
        });
    </script>
</head>
<body>
    <div class="login-container">
        <div class="login-brand">
▄▀█ █▄ █ █▀▀ ▄▀█ █▀▄
█▀█ █ ▀█ █▄▄ █▀█ █▀▀
        </div>
        <div class="login-subtitle">» A LIBERTARIAN RSS READER «</div>
        
        <div class="login-content">
            <form class="login-form" onsubmit="handleLogin(event)">
                <input 
                    type="text" 
                    id="username" 
                    class="login-input" 
                    placeholder="USUARIO" 
                    onkeypress="handleKeyPress(event)"
                    autocomplete="username">
                <input 
                    type="password" 
                    id="password" 
                    class="login-input" 
                    placeholder="CONTRASEÑA" 
                    onkeypress="handleKeyPress(event)"
                    autocomplete="current-password">
                <button type="submit" id="login-button" class="login-button">
                    ACCEDER
                </button>
                <div id="login-error" class="login-error" style="display: none;"></div>
                <div class="login-info">
                    Credenciales por defecto:<br>
                    <strong>admin</strong> / admin123<br>
                    <strong>ancap</strong> / libertad
                </div>
            </form>
        </div>
    </div>
</body>
</html>`

	w.Write([]byte(html))
}

func loginAPIHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var loginReq struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&loginReq); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if validateLogin(loginReq.Username, loginReq.Password) {
		sessionID := createSession(loginReq.Username)

		// Crear cookie de sesión
		cookie := &http.Cookie{
			Name:     "session_id",
			Value:    sessionID,
			Expires:  time.Now().Add(SESSION_DURATION),
			HttpOnly: true,
			Path:     "/",
		}
		http.SetCookie(w, cookie)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "Login exitoso",
		})

		log.Printf("✅ Login exitoso para usuario: %s", loginReq.Username)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "Credenciales inválidas",
		})

		log.Printf("❌ Login fallido para usuario: %s", loginReq.Username)
	}
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session_id")
	if err == nil {
		sessionMutex.Lock()
		delete(sessions, cookie.Value)
		sessionMutex.Unlock()
	}

	// Eliminar cookie
	deleteCookie := &http.Cookie{
		Name:     "session_id",
		Value:    "",
		Expires:  time.Now().Add(-1 * time.Hour),
		HttpOnly: true,
		Path:     "/",
	}
	http.SetCookie(w, deleteCookie)

	http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
}

func main() {
	// Limpiar sesiones expiradas cada hora
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			clearExpiredSessions()
		}
	}()

	mux := http.NewServeMux()

	// Rutas públicas (sin autenticación)
	mux.HandleFunc("/login", loginPageHandler)
	mux.HandleFunc("/api/login", loginAPIHandler)
	mux.HandleFunc("/api/preload-feeds", preloadFeedsHandler)
	mux.HandleFunc("/static/", staticHandler)

	// Rutas protegidas (con autenticación)
	mux.Handle("/", authMiddleware(http.HandlerFunc(homeHandler)))
	mux.Handle("/add", authMiddleware(http.HandlerFunc(addHandler)))
	mux.Handle("/favorite", authMiddleware(http.HandlerFunc(favoriteHandler)))
	mux.Handle("/api/favorites", authMiddleware(http.HandlerFunc(apiFavoritesHandler)))
	mux.Handle("/api/scrape-article", authMiddleware(http.HandlerFunc(scrapeArticleHandler)))
	mux.Handle("/clear-cache", authMiddleware(http.HandlerFunc(clearCacheHandler)))
	mux.Handle("/logout", authMiddleware(http.HandlerFunc(logoutHandler)))

	log.Println("🚀 Starting ANCAP WEB Server with Authentication...")
	log.Println("🌐 Server running at http://localhost:8083")
	log.Println("🔐 Default users: admin/admin123, ancap/libertad")

	if err := http.ListenAndServe(":8083", gzipMiddleware(mux)); err != nil {
		log.Fatalf("❌ Server failed to start: %v", err)
	}
}
