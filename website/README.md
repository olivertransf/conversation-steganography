# How it works site

Static walkthrough of the Conversation Stenography pipeline.

```sh
open index.html
# or
python3 -m http.server 8080
```

## GitHub Pages

Branch deploy only allows `/` or `/docs`. This site lives in `website/`, so deploy with **Actions**:

1. Repo **Settings → Pages → Build and deployment → Source: GitHub Actions**
2. Push to `main` (or run the **Deploy how-it-works site** workflow manually)
3. Site: https://olivertransf.github.io/conversation-steganography/

## Netlify

Publish directory `website`, empty build command.
