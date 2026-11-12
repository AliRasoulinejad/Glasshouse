import { Fragment } from 'react';
import { loadArticle, slugs } from '@/lib/articles';
import { InlineDiagram, ScrollPanel } from '@/components/ScrollPanel';
import { LabSection } from '@/components/LabSection';

export const dynamicParams = false;

export function generateStaticParams() {
  return slugs().map((slug) => ({ slug }));
}

export async function generateMetadata({ params }: { params: Promise<{ slug: string }> }) {
  const { slug } = await params;
  return { title: `${loadArticle(slug).title} · Glasshouse` };
}

export default async function ArticlePage({ params }: { params: Promise<{ slug: string }> }) {
  const { slug } = await params;
  const article = loadArticle(slug);
  const steps = article.sections.filter((s) => s.startsStep).map((s) => ({ id: s.step, label: s.heading }));
  return (
    <main className="article">
      <div className="article-grid">
        <article className="reading">
          <h1>{article.title}</h1>
          {article.sections.map((s) => (
            <Fragment key={s.id}>
              {/* Article HTML is built from repo Markdown at build time, never from API data. */}
              <section id={s.id} data-step={s.step} dangerouslySetInnerHTML={{ __html: s.html }} />
              {s.startsStep && <InlineDiagram slug={slug} step={s.step} />}
            </Fragment>
          ))}
        </article>
        <ScrollPanel slug={slug} steps={steps} />
      </div>
      <LabSection actions={article.labActions} />
    </main>
  );
}
