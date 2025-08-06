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

type FeedCache struct {
	mutex sync.RWMutex
	feeds map[string]CachedFeed
}

var globalCache = &FeedCache{
	feeds: make(map[string]CachedFeed),
}

const CACHE_DURATION = 1 * time.Minute

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
    <title>ANCAP WEB - ` + time.Now().Format("15:04:05") + ` [AIR]</title>
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
        
        /* Loading Screen */
        .loading-screen {
            position: fixed;
            top: 0;
            left: 0;
            width: 100%;
            height: 100%;
            background: #000;
            color: #00ff00;
            display: flex;
            flex-direction: column;
            justify-content: center;
            align-items: center;
            z-index: 9999;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
        }
        
        .loading-screen.hidden {
            display: none;
        }
        
        .loading-brand {
            font-size: 32px;
            font-weight: bold;
            margin-bottom: 10px;
            color: #00ff00;
            text-align: center;
        }
        
        .loading-subtitle {
            font-size: 16px;
            color: #888;
            margin-bottom: 30px;
            text-align: center;
        }
        
        .loading-text {
            font-size: 14px;
            color: #00ff00;
            margin-bottom: 20px;
        }
        
        .loading-bar {
            width: 300px;
            height: 4px;
            background: #333;
            border: 1px solid #00ff00;
            position: relative;
            overflow: hidden;
        }
        
        .loading-progress {
            height: 100%;
            background: #00ff00;
            width: 0%;
            transition: width 0.3s ease;
        }
        
        .loading-dots::after {
            content: '';
            animation: dots 1.5s infinite;
        }
        
        @keyframes dots {
            0%, 20% { content: ''; }
            40% { content: '.'; }
            60% { content: '..'; }
            80%, 100% { content: '...'; }
        }
        .container { 
            max-width: 720px; 
            margin: 0; 
            text-align: left;
            width: 100%;
        }
        .tabs {
            display: flex;
            width: 100%;
            margin-bottom: 20px;
            border-bottom: 1px solid #00ff00;
        }
        .tab {
            flex: 1;
            text-align: center;
            padding: 10px;
            color: #00ff00;
            text-decoration: none;
            border: 1px solid #333;
            background: #000;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-size: 12px;
            cursor: pointer;
        }
        .tab:hover {
            background: #111;
        }
        .tab.tab-active {
            background: #000;
            color: #00ff00;
            font-weight: bold;
            border-bottom: 1px solid #000;
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
            background: #111;
            color: #00ff00;
            padding: 15px;
            margin: 5px 0;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-size: 14px;
            line-height: 1.4;
            border-left: 3px solid #00ff00;
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
            border-left: 3px solid #00ff00;
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
                
                // Cargar contenido si no está cargado
                if (!content.innerHTML.trim() || content.innerHTML === 'Cargando contenido...') {
                    loadArticleContent(index);
                }
            }
        }       } else {
                    setTimeout(hideLoadingScreen, 500);
                }
            }
            
            updateProgress();
        }
        
        // Ocultar loading screen
        function hideLoadingScreen() {
            const loadingScreen = document.getElementById('loading-screen');
            if (loadingScreen) {
                loadingScreen.classList.add('hidden');
            }
        }
        
        // Función mejorada para cargar contenido completo del artículo
        function loadArticleContent(index) {
            const article = articles[index];
            const content = document.getElementById('content-' + index);
            
            content.innerHTML = 'Cargando contenido completo...';
            
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
            let articleText = fullContent || article.description || 'Contenido no disponible.';
            
            // Limpiar HTML si es necesario
            if (!fullContent) {
                articleText = articleText.replace(/<[^>]*>/g, '');
                articleText = articleText.replace(/&[^;]+;/g, ' ');
                articleText = articleText.trim();
                
                if (articleText.length > 1200) {
                    articleText = articleText.substring(0, 1200) + '...';
                }
            }
            
            // Determinar posición de botones según configuración
            const buttonsPosition = localStorage.getItem('buttonsPosition') || 'right';
            
            const buttonStyle = buttonsPosition === 'right' ? 
                'position: absolute; right: 15px; top: 15px; text-align: right;' :
                'position: absolute; left: 15px; top: 15px; text-align: left;';
            
            const buttonsHTML = '<div style="' + buttonStyle + '">' +
                '<div id="saved-btn-' + index + '" onclick="toggleSaved(' + index + ', event)" style="color: ' + (article.isSaved ? '#FFD700' : '#00ff00') + '; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">' + (article.isSaved ? '[GUARDADO]' : '[GUARDAR]') + '</div>' +
                '<div id="loved-btn-' + index + '" onclick="toggleLoved(' + index + ')" style="color: ' + (article.isLoved ? '#FF69B4' : '#00ff00') + '; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">' + (article.isLoved ? '[FAVORITO]' : '[MARCAR FAV]') + '</div>' +
                '<div onclick="shareArticle(' + index + ')" style="color: #00ff00; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">[COMPARTIR]</div>' +
                '<div onclick="window.open(\'' + article.link + '\', \'_blank\')" style="color: #ffff00; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px;">[LEER ORIGINAL]</div>' +
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
                    imageHTML = '<div style="margin-bottom: 15px; text-align: center;">';
                    images.forEach(imgSrc => {
                        imageHTML += '<img src="' + imgSrc + '" style="max-width: 200px; max-height: 150px; margin: 5px; border: 1px solid #333; object-fit: cover; display: none;" onerror="this.style.display=\'none\'" onload="this.style.display=\'inline-block\'">';
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
                              '<div style="margin-bottom: 15px;"><a href="' + article.link + '" target="_blank" style="color: #ffff00; text-decoration: none; font-weight: bold;">' + article.title + '</a></div>' +
                              '<div style="line-height: 1.6; color: #ccc;">' + articleText + '</div>' +
                              (fullContent ? '<div style="margin-top: 15px; padding-top: 10px; border-top: 1px solid #333; color: #888; font-size: 11px;">CONTENIDO COMPLETO CARGADO</div>' : '<div style="margin-top: 15px; padding-top: 10px; border-top: 1px solid #333; color: #888; font-size: 11px;">Haz doble clic en la línea para ver el artículo completo en el sitio original</div>') +
                              '</div>';
        }
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
                
                // Cargar contenido si no está cargado
                if (!content.innerHTML.trim()) {
                    content.innerHTML = 'Cargando contenido...';
                    loadArticleContent(index);
                }
            } else {
                // Al cerrar un artículo, mantener el selectedIndex actual
                // selectedIndex = -1; // Comentamos esta línea para mantener la posición
            }
        }
        
        function loadArticleContent(index) {
            const article = articles[index];
            const content = document.getElementById('content-' + index);
            
            // Simular carga de contenido
            setTimeout(() => {
                // Limpiar la descripción de HTML tags
                let cleanDescription = article.description || 'Contenido no disponible. Haz doble clic en la línea para ver el artículo completo.';
                cleanDescription = cleanDescription.replace(/<[^>]*>/g, ''); // Quitar HTML tags
                cleanDescription = cleanDescription.replace(/&[^;]+;/g, ' '); // Quitar HTML entities
                cleanDescription = cleanDescription.trim();
                
                // Truncar si es muy largo
                if (cleanDescription.length > 800) {
                    cleanDescription = cleanDescription.substring(0, 800) + '...';
                }
                
                // Determinar posición de botones según configuración
                const buttonsPosition = localStorage.getItem('buttonsPosition') || 'right';
                
                // Botones con posición configurable
                const buttonStyle = buttonsPosition === 'right' ? 
                    'position: absolute; right: 15px; top: 15px; text-align: right;' :
                    'position: absolute; left: 15px; top: 15px; text-align: left;';
                
                const buttonsHTML = '<div style="' + buttonStyle + '">' +
                    '<div id="saved-btn-' + index + '" onclick="toggleSaved(' + index + ', event)" style="color: ' + (article.isSaved ? '#FFD700' : '#00ff00') + '; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">' + (article.isSaved ? '[GUARDADO]' : '[GUARDAR]') + '</div>' +
                    '<div id="loved-btn-' + index + '" onclick="toggleLoved(' + index + ')" style="color: ' + (article.isLoved ? '#FF69B4' : '#00ff00') + '; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">' + (article.isLoved ? '[FAVORITO]' : '[MARCAR FAV]') + '</div>' +
                    '<div onclick="shareArticle(' + index + ')" style="color: #00ff00; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px;">[COMPARTIR]</div>' +
                    '</div>';
                
                // Extraer y procesar imágenes del contenido original (sin limpiar)
                let imageHTML = '';
                const originalDescription = article.description || '';
                
                if (originalDescription && originalDescription.trim().length > 0) {
                    const imgRegex = /<img[^>]+src=["\']([^"\']+)["\'][^>]*>/gi;
                    let match;
                    
                    // Buscar múltiples imágenes
                    const images = [];
                    while ((match = imgRegex.exec(originalDescription)) !== null && images.length < 3) {
                        // Validar que la URL de la imagen sea válida
                        const imgSrc = match[1];
                        if (imgSrc && (imgSrc.startsWith('http://') || imgSrc.startsWith('https://') || imgSrc.startsWith('//'))) {
                            images.push(imgSrc);
                        }
                    }
                    
                    // Generar HTML para imágenes solo si hay imágenes válidas
                    if (images.length > 0) {
                        imageHTML = '<div style="margin-bottom: 15px; text-align: center;">';
                        images.forEach(imgSrc => {
                            imageHTML += '<img src="' + imgSrc + '" style="max-width: 150px; max-height: 100px; margin: 5px; border: 1px solid #333; object-fit: cover; display: none;" onerror="this.style.display=\'none\'" onload="this.style.display=\'inline-block\'; console.log(\'Imagen cargada: \' + this.src)">';
                        });
                        imageHTML += '</div>';
                    }
                }
                
                // Ajustar padding según posición de botones
                const contentPadding = buttonsPosition === 'right' ? 
                    'padding-right: 100px;' : 
                    'padding-left: 100px;';
                
                content.innerHTML = buttonsHTML +
                                  '<div style="' + contentPadding + '">' +
                                  (imageHTML || '') +
                                  '<div style="margin-bottom: 10px; color: #888; font-size: 12px;">' + article.date + ' | ' + article.source + '</div>' +
                                  '<div style="margin-bottom: 10px;"><a href="' + article.link + '" target="_blank" style="color: #ffff00; text-decoration: none;">' + article.title + '</a></div>' +
                                  cleanDescription +
                                  '</div>';
            }, 300);
        }
        
        document.addEventListener('keydown', function(event) {
            // Obtener la página activa
            const activePage = document.querySelector('.page-content[style*="display: block"]').id.replace('-content', '');
            
            let totalArticles;
            let articleContainers;
            if (activePage === 'feeds') {
                articleContainers = document.querySelectorAll('#feeds-content .article-container');
                totalArticles = articleContainers.length;
            } else if (activePage === 'saved') {
                articleContainers = document.querySelectorAll('#saved-articles-list .article-container');
                totalArticles = articleContainers.length;
            } else if (activePage === 'loved') {
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
                        // Expandir artículo según la página
                        if (activePage === 'feeds') {
                            toggleArticle(selectedIndex);
                        } else if (activePage === 'saved' || activePage === 'loved') {
                            // Para saved/loved necesitamos usar un ID único basado en el link
                            const container = articleContainers[selectedIndex];
                            if (container) {
                                const link = container.querySelector('.full-line-link').getAttribute('data-link');
                                const prefix = activePage === 'saved' ? 'content-saved-' : 'content-loved-';
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
                        // Si hay artículo expandido, ir al siguiente y expandirlo
                        if (selectedIndex < totalArticles - 1) {
                            selectedIndex++;
                            highlightSelected();
                            // Expandir según la página activa
                            if (activePage === 'feeds') {
                                toggleArticle(selectedIndex);
                            } else if (activePage === 'saved' || activePage === 'loved') {
                                const container = articleContainers[selectedIndex];
                                if (container) {
                                    const link = container.querySelector('.full-line-link').getAttribute('data-link');
                                    const prefix = activePage === 'saved' ? 'content-saved-' : 'content-loved-';
                                    const contentId = prefix + btoa(link).replace(/=/g, '').substring(0, 10);
                                    toggleArticleGeneric(container, contentId, link, true); // forceOpen = true para navegación
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
                        // Si hay artículo expandido, ir al anterior y expandirlo
                        if (selectedIndex > 0) {
                            selectedIndex--;
                            highlightSelected();
                            // Expandir según la página activa
                            if (activePage === 'feeds') {
                                toggleArticle(selectedIndex);
                            } else if (activePage === 'saved' || activePage === 'loved') {
                                const container = articleContainers[selectedIndex];
                                if (container) {
                                    const link = container.querySelector('.full-line-link').getAttribute('data-link');
                                    const prefix = activePage === 'saved' ? 'content-saved-' : 'content-loved-';
                                    const contentId = prefix + btoa(link).replace(/=/g, '').substring(0, 10);
                                    toggleArticleGeneric(container, contentId, link, true); // forceOpen = true para navegación
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
                        if (activePage === 'feeds') {
                            link = articles[selectedIndex].link;
                        } else if (activePage === 'saved' || activePage === 'loved') {
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
                        if (activePage === 'feeds') {
                            toggleSaved(selectedIndex);
                        } else if (activePage === 'saved' || activePage === 'loved') {
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
                        if (activePage === 'feeds') {
                            toggleLoved(selectedIndex);
                        } else if (activePage === 'saved' || activePage === 'loved') {
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
                // Buscar en la página activa
                const activePage = document.querySelector('.page-content[style*="display: block"]').id.replace('-content', '');
                let lines;
                
                if (activePage === 'feeds') {
                    lines = document.querySelectorAll('#feeds-content .article-line');
                } else if (activePage === 'saved') {
                    lines = document.querySelectorAll('#saved-articles-list .article-line');
                } else if (activePage === 'loved') {
                    lines = document.querySelectorAll('#loved-articles-list .article-line');
                }
                
                if (lines && lines[selectedIndex]) {
                    lines[selectedIndex].classList.add('selected');
                    lines[selectedIndex].scrollIntoView({ behavior: 'smooth', block: 'center' });
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
                
                // Cargar contenido si no está cargado
                if (!content.innerHTML.trim()) {
                    content.innerHTML = 'Cargando contenido...';
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
                // Limpiar la descripción de etiquetas HTML
                let cleanDescription = description || 'Contenido no disponible. Haz doble clic en la línea para ver el artículo completo.';
                cleanDescription = cleanDescription.replace(/<[^>]*>/g, ''); // Quitar etiquetas HTML
                cleanDescription = cleanDescription.replace(/&[^;]+;/g, ' '); // Quitar entidades HTML
                cleanDescription = cleanDescription.trim();
                
                // Truncar si es muy largo
                if (cleanDescription.length > 800) {
                    cleanDescription = cleanDescription.substring(0, 800) + '...';
                }
                
                // Determinar posición de botones según configuración
                const buttonsPosition = localStorage.getItem('buttonsPosition') || 'right';
                
                // Botones con posición configurable
                const buttonStyle = buttonsPosition === 'right' ? 
                    'position: absolute; right: 15px; top: 15px; text-align: right;' :
                    'position: absolute; left: 15px; top: 15px; text-align: left;';
                
                const buttonsHTML = '<div style="' + buttonStyle + '">' +
                    '<div onclick="toggleSavedByLink(\'' + link + '\')" style="color: #FFD700; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">[GUARDADO]</div>' +
                    '<div onclick="toggleLovedByLink(\'' + link + '\')" style="color: #FF69B4; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">[FAVORITO]</div>' +
                    '<div onclick="shareArticleByLink(\'' + link + '\')" style="color: #00ff00; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px;">[COMPARTIR]</div>' +
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
                                  cleanDescription +
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
                    title: 'Artículo interesante',
                    url: link
                });
            } else {
                // Fallback: copiar al clipboard
                navigator.clipboard.writeText(link).then(() => {
                    alert('Enlace copiado al portapapeles');
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
                button.textContent = article.isSaved ? '[GUARDADO]' : '[GUARDAR]';
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
                button.textContent = article.isLoved ? '[FAVORITO]' : '[MARCAR FAV]';
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
                lovedList.innerHTML = '<p style="color: #888;">No hay artículos favoritos aún.</p>';
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
                savedList.innerHTML = '<p style="color: #888;">No hay artículos guardados aún.</p>';
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
                // Fallback: copiar al clipboard
                navigator.clipboard.writeText(article.link).then(() => {
                    alert('Enlace copiado al portapapeles');
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
            if (event) {
                event.preventDefault();
            }
            
            // Ocultar todos los contenidos
            const contentIds = ['feeds-content', 'import-content', 'saved-content', 'loved-content', 'config-content'];
            contentIds.forEach(id => {
                const element = document.getElementById(id);
                if (element) element.style.display = 'none';
            });
            
            // Quitar clase activa de todas las pestañas
            document.querySelectorAll('.tab').forEach(tab => {
                tab.classList.remove('tab-active');
            });
            
            // Mostrar el contenido solicitado
            const targetContent = document.getElementById(pageName + '-content');
            if (targetContent) {
                targetContent.style.display = 'block';
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
            // Mostrar pantalla de carga al inicio
            showLoadingScreen();
            
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
                    description: span.getAttribute('data-description') || ''
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
    <!-- Pantalla de carga -->
    <div id="loading-screen" class="loading-screen">
        <div class="loading-brand">ANCAP WEB</div>
        <div class="loading-subtitle">&lt;A LIBERTARIAN RSS READER&gt;</div>
        <div class="loading-text">LOADING<span class="loading-dots"></span></div>
        <div class="loading-bar">
            <div class="loading-progress"></div>
        </div>
    </div>
    
    <div class="container">
        <h1>ANCAP WEB <span style="color: #888; font-size: 12px;">A LIBERTARIAN RSS READER</span></h1>
        <div class="tabs">
            <a class="tab tab-active" href="#" onclick="showPage('feeds', event)">FEEDS <span id="feeds-count"></span> <span class="tab-shortcut">(F1)</span></a>
            <a class="tab" href="#" onclick="showPage('saved', event)">GUARDADOS <span id="saved-count"></span> <span class="tab-shortcut">(F2)</span></a>
            <a class="tab" href="#" onclick="showPage('loved', event)">FAVORITOS <span id="loved-count"></span> <span class="tab-shortcut">(F3)</span></a>
            <a class="tab" href="#" onclick="showPage('config', event)">CONFIG <span class="tab-shortcut">(F4)</span></a>
        </div>
        <div class="info">` + time.Now().Format("02/01/2006 15:04") + ` | ` + fmt.Sprintf("%d artículos cargados", len(data.Articles)) + `</div>
        
        <!-- Contenido principal de feeds -->
        <div id="feeds-content" class="page-content" style="display: block;">`

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
                    <span class="meta">` + article.Date + ` | ` + article.Source + ` | </span>
                    <span class="title">` + title + `</span>
                </span>
            </div>
            <div id="content-` + fmt.Sprintf("%d", i) + `" class="article-content"></div>
        </div>`
	}

	html += `        </div>
        
        <!-- Página de Saved -->
        <div id="saved-content" class="page-content" style="display: none;">
            <div class="page-header">ARTÍCULOS GUARDADOS (F2)</div>
            <p>Lista de artículos marcados como guardados.</p>
            <div id="saved-articles-list">
                <p style="color: #888;">No hay artículos guardados aún.</p>
            </div>
        </div>
        
        <!-- Página de Loved -->
        <div id="loved-content" class="page-content" style="display: none;">
            <div class="page-header">ARTÍCULOS FAVORITOS (F3)</div>
            <p>Lista de artículos marcados como favoritos.</p>
            <div id="loved-articles-list">
                <p style="color: #888;">No hay artículos favoritos aún.</p>
            </div>
        </div>
        
        <!-- Página de Config -->
        <div id="config-content" class="page-content" style="display: none;">
            <div class="page-header">CONFIGURACIÓN (F4)</div>
            
            <div class="config-section">
                <h3>Feeds RSS</h3>
                <label class="config-label">URL del Feed RSS:</label>
                <input type="text" class="config-input" placeholder="https://example.com/feed.xml">
                <button onclick="alert('Función por implementar')" style="background: none; border: 1px solid #00ff00; color: #00ff00; padding: 3px 8px; font-family: 'JetBrains Mono', monospace; font-size: 12px; cursor: pointer; margin-left: 10px;">Agregar</button>
                
                <br><br>
                <label class="config-label">Importar archivo OPML:</label>
                <input type="file" class="config-input" accept=".opml,.xml">
                <button onclick="alert('Función por implementar')" style="background: none; border: 1px solid #00ff00; color: #00ff00; padding: 3px 8px; font-family: 'JetBrains Mono', monospace; font-size: 12px; cursor: pointer; margin-left: 10px;">Importar</button>
            </div>
            
            <div class="config-section">
                <h3>Interfaz</h3>
                <label class="config-label">Posición de botones en artículos:</label>
                <select id="buttonsConfigModal" onchange="changeButtonsPosition()" class="config-select">
                    <option value="right">Derecha</option>
                    <option value="left">Izquierda</option>
                </select>
            </div>
            
            <div class="config-section">
                <h3>Feeds</h3>
                <label class="config-label">Intervalo de actualización (minutos):</label>
                <input type="number" class="config-input" value="30" min="5" max="1440">
            </div>
            
            <div class="config-section">
                <h3>Apariencia</h3>
                <label class="config-label">Tema:</label>
                <select class="config-select">
                    <option value="green">Verde (actual)</option>
                    <option value="blue">Azul</option>
                    <option value="amber">Ámbar</option>
                </select>
            </div>
            
            <div class="config-section">
                <h3>Atajos de teclado</h3>
                <p><strong>F1-F4:</strong> Cambiar entre páginas</p>
                <p><strong>↑/↓, J/K:</strong> Navegar artículos</p>
                <p><strong>Space/Enter:</strong> Expandir artículo</p>
                <p><strong>S/L/C:</strong> Guardar/Favorito/Compartir</p>
                <p><strong>Escape:</strong> Volver a FEEDS</p>
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

	data := TemplateData{
		Articles: allArticles,
	}

	elapsed := time.Since(startTime)
	log.Printf("⚡ Home handler completed in %v with %d articles (CACHE + PARALLEL)", elapsed, len(allArticles))
	renderHomePage(w, data)
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

	// Intentar hacer scraping del contenido completo
	content, err := scrapeArticleContent(request.URL)
	if err != nil {
		log.Printf("❌ Error scraping article: %v", err)
		http.Error(w, "Could not scrape article", http.StatusInternalServerError)
		return
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

	if len(content) < 100 {
		return "", fmt.Errorf("extracted content too short, probably failed")
	}

	return content, nil
}

func extractMainContent(html string) string {
	// Selectores comunes para contenido principal
	contentSelectors := []string{
		"<article[^>]*>([\\s\\S]*?)</article>",
		"<div[^>]*class[^>]*[\"'].*?content.*?[\"'][^>]*>([\\s\\S]*?)</div>",
		"<div[^>]*class[^>]*[\"'].*?post.*?[\"'][^>]*>([\\s\\S]*?)</div>",
		"<div[^>]*class[^>]*[\"'].*?entry.*?[\"'][^>]*>([\\s\\S]*?)</div>",
		"<div[^>]*class[^>]*[\"'].*?main.*?[\"'][^>]*>([\\s\\S]*?)</div>",
		"<main[^>]*>([\\s\\S]*?)</main>",
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
	bodyRe := regexp.MustCompile("<body[^>]*>([\\s\\S]*?)</body>")
	matches := bodyRe.FindStringSubmatch(html)
	if len(matches) > 1 {
		return matches[1]
	}

	return html
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

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", homeHandler)
	mux.HandleFunc("/add", addHandler)
	mux.HandleFunc("/favorite", favoriteHandler)
	mux.HandleFunc("/api/favorites", apiFavoritesHandler)
	mux.HandleFunc("/api/scrape-article", scrapeArticleHandler)
	mux.HandleFunc("/clear-cache", clearCacheHandler)
	mux.HandleFunc("/static/", staticHandler)

	log.Println("🚀 Starting ANCAP WEB Server...")
	log.Println("🌐 Server running at http://localhost:8083")

	if err := http.ListenAndServe(":8083", gzipMiddleware(mux)); err != nil {
		log.Fatalf("❌ Server failed to start: %v", err)
	}
}
