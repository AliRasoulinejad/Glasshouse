import { loadArticle, slugs } from '@/lib/articles';

export default function Home() {
  const articles = slugs().map(loadArticle);
  return (
    <main className="home">
      <h1>Glasshouse</h1>
      <p>Articles that show PostgreSQL's real internals, captured from a real running instance, with your own live database at the end.</p>
      <ul>
        {articles.map((a) => <li key={a.slug}><a href={`/articles/${a.slug}/`}>{a.title}</a></li>)}
      </ul>
    </main>
  );
}
