#!/usr/bin/env python3
"""Build the final PDF and vector figures from docs/final-project-report.md.

Requires reportlab. The Markdown source is authoritative. Figures are native
vector drawings in the PDF and exported as SVG for the repository version.
"""
from __future__ import annotations

import hashlib
import html
import re
from pathlib import Path

from reportlab.graphics import renderSVG
from reportlab.graphics.shapes import Drawing, Line, Polygon, Rect, String
from reportlab.lib import colors
from reportlab.lib.enums import TA_CENTER, TA_LEFT
from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import ParagraphStyle, getSampleStyleSheet
from reportlab.lib.utils import simpleSplit
from reportlab.platypus import (
    BaseDocTemplate, Frame, KeepTogether, LongTable, PageBreak, PageTemplate,
    Paragraph, Preformatted, Spacer, TableStyle,
)
from reportlab.platypus.tableofcontents import TableOfContents

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / 'docs/final-project-report.md'
OUT = ROOT / 'docs/report'
WIDTH = A4[0] - 104
INK = colors.HexColor('#182C40')
BLUE = colors.HexColor('#355F86')
TEAL = colors.HexColor('#287467')
GREY = colors.HexColor('#D9D9D9')
MUTED = colors.HexColor('#566574')
PALE = colors.HexColor('#EDF2F7')


def label(d, x, y, text, size=9.5, color=INK, bold=False, center=False):
    d.add(String(x, y, text, fontName='Helvetica-Bold' if bold else 'Helvetica',
                 fontSize=size, fillColor=color,
                 textAnchor='middle' if center else 'start'))


def box(d, x, y, w, h, title, subtitle='', fill=colors.white):
    d.add(Rect(x, y, w, h, fillColor=fill, strokeColor=BLUE, strokeWidth=.8))
    lines = title.split('\n') + ([subtitle] if subtitle else [])
    yy = y + h / 2 + (len(lines) - 1) * 6 - 3
    for i, line in enumerate(lines):
        label(d, x + w / 2, yy - i * 12, line, 9, center=True, bold=i == 0)


def arrow(d, x1, y1, x2, y2, color=BLUE, dashed=False):
    import math
    d.add(Line(x1, y1, x2, y2, strokeColor=color, strokeWidth=1,
               strokeDashArray=[3, 3] if dashed else None))
    a = math.atan2(y2 - y1, x2 - x1)
    points = [x2, y2, x2 - 5 * math.cos(a - .45), y2 - 5 * math.sin(a - .45),
              x2 - 5 * math.cos(a + .45), y2 - 5 * math.sin(a + .45)]
    d.add(Polygon(points, fillColor=color, strokeColor=color))


def figures():
    out = {}
    d = Drawing(WIDTH, 153)
    box(d, 0, 85, 130, 50, 'Worker', 'durable intent', PALE)
    box(d, 180, 85, 130, 50, 'External service', 'commits effect', PALE)
    box(d, 360, 85, 130, 50, 'Worker crashes', 'receipt absent')
    arrow(d, 130, 110, 180, 110)
    arrow(d, 310, 110, 360, 110, colors.HexColor('#A8584A'), True)
    label(d, 0, 51, 'Local knowledge: unfinished', 11, bold=True)
    label(d, 0, 27, 'Possible external states: not committed OR already committed', 10)
    label(d, 0, 7, 'Recovery must distinguish these states using evidence.', 9.5, MUTED)
    out['ambiguity'] = d

    d = Drawing(WIDTH, 249)
    box(d, 0, 175, 125, 53, 'Workflow worker', 'prepare / authorize')
    box(d, 179, 175, 137, 53, 'API and policy', 'atomic decisions', PALE)
    box(d, 368, 175, 123, 53, 'Operation store', 'intent + audit', PALE)
    arrow(d, 125, 202, 179, 202)
    arrow(d, 316, 202, 368, 202)
    box(d, 179, 72, 137, 53, 'Coordinator', 'claim + verify')
    arrow(d, 247, 125, 247, 175)
    box(d, 0, 72, 125, 53, 'Email service', 'independent effects')
    arrow(d, 61, 175, 61, 125)
    label(d, 3, 145, 'dispatch ID', 8.5)
    arrow(d, 179, 99, 125, 99)
    label(d, 135, 113, 'verify', 8)
    box(d, 368, 72, 123, 53, 'Effect ledger', 'evaluator access', colors.HexColor('#EDF5F1'))
    d.add(Line(61, 72, 61, 45, strokeColor=TEAL, strokeWidth=1))
    d.add(Line(61, 45, 429, 45, strokeColor=TEAL, strokeWidth=1))
    arrow(d, 429, 45, 429, 72, TEAL)
    label(d, 0, 13, 'Workers and verifiers use the service interface, not ground-truth tables.', 9.2, MUTED)
    out['architecture'] = d

    d = Drawing(WIDTH, 218)
    cases = [('Matching authoritative completion', 'Reconciled', TEAL),
             ('No dispatch authorization and no conflict', 'Retryable', BLUE),
             ('Verified durable replay contract', 'Retryable', BLUE),
             ('Transient failure below attempt limit', 'Defer', BLUE),
             ('Otherwise insufficient or conflicting evidence', 'Uncertain', colors.HexColor('#A8584A'))]
    for i, (premise, result, col) in enumerate(cases):
        y = 177 - i * 39
        box(d, 0, y, 335, 29, premise)
        box(d, 390, y, 101, 29, result, fill=PALE)
        arrow(d, 335, y + 14.5, 390, y + 14.5, col)
    out['policy'] = d

    d = Drawing(WIDTH, 216)
    x0, y0, chartw, charth = 48, 44, 387, 136
    for n in [0, 25, 50, 75, 100]:
        yy = y0 + charth * n / 100
        d.add(Line(x0, yy, x0 + chartw, yy, strokeColor=GREY, strokeWidth=.5))
        label(d, x0 - 12, yy - 3, str(n), 8, MUTED, center=True)
    vals = [('Blind retry', 100, '#B57565'), ('Checkpoint', 100, '#8396AA'), ('Watchkeeper', 0, '#287467')]
    for i, (name, value, color) in enumerate(vals):
        xx = x0 + 39 + i * 129
        if value:
            d.add(Rect(xx, y0, 65, charth * value / 100,
                       fillColor=colors.HexColor(color), strokeColor=None))
        label(d, xx + 32, y0 + charth * value / 100 + 9, str(value), 11, bold=True, center=True)
        label(d, xx + 32, 24, name, 9.5, center=True)
    label(d, 48, 201, 'Duplicate trials out of 100', 10, MUTED)
    out['results'] = d
    return out


def inline(text):
    text = html.escape(text)
    text = re.sub(r'`([^`]+)`', r'<font name="Courier" size="8.5">\1</font>', text)
    text = re.sub(r'\*\*([^*]+)\*\*', r'<b>\1</b>', text)
    text = re.sub(r'\[([^]]+)\]\((https?://[^)]+)\)', r'<link href="\2" color="#355F86">\1</link>', text)
    text = re.sub(r'\[([^]]+)\]\(([^)]+)\)', r'\1', text)
    return text.replace(' · ', ' | ')


class Report(BaseDocTemplate):
    def afterFlowable(self, flow):
        if isinstance(flow, Paragraph) and flow.style.name == 'Chapter':
            title = flow.getPlainText()
            if title == 'Contents':
                return
            key = hashlib.sha256(title.encode()).hexdigest()[:16]
            self.canv.bookmarkPage(key)
            self.canv.addOutlineEntry(title, key, 0)
            self.notify('TOCEntry', (0, title, self.page, key))


def footer(canvas, doc):
    canvas.saveState()
    canvas.setStrokeColor(GREY)
    canvas.line(52, 43, A4[0] - 52, 43)
    canvas.setFont('Helvetica', 8)
    canvas.setFillColor(MUTED)
    canvas.drawString(52, 29, 'Watchkeeper | Final Project Report')
    canvas.drawRightString(A4[0] - 52, 29, str(doc.page))
    canvas.restoreState()


def build():
    OUT.mkdir(parents=True, exist_ok=True)
    assets = OUT / 'assets'
    assets.mkdir(exist_ok=True)
    figs = figures()
    for name, drawing in figs.items():
        renderSVG.drawToFile(drawing, str(assets / (name + '.svg')))
    styles = getSampleStyleSheet()
    styles.add(ParagraphStyle(name='ReportBody', fontName='Helvetica', fontSize=10.2,
                              leading=14.8, spaceAfter=8, textColor=colors.black,
                              allowWidows=0, allowOrphans=0))
    styles.add(ParagraphStyle(name='ReportTitle', fontName='Helvetica-Bold', fontSize=27,
                              leading=32, spaceAfter=17, textColor=colors.black))
    styles.add(ParagraphStyle(name='Chapter', fontName='Helvetica-Bold', fontSize=15,
                              leading=19, spaceBefore=18, spaceAfter=10,
                              textColor=colors.black, keepWithNext=True))
    styles.add(ParagraphStyle(name='Subheading', fontName='Helvetica-Bold', fontSize=11.2,
                              leading=15, spaceBefore=11, spaceAfter=6,
                              textColor=colors.black, keepWithNext=True))
    styles.add(ParagraphStyle(name='Caption', fontName='Helvetica', fontSize=8.8,
                              leading=12, textColor=MUTED, spaceBefore=6, spaceAfter=12))
    styles.add(ParagraphStyle(name='Cell', fontName='Helvetica', fontSize=8.7,
                              leading=11.5, textColor=colors.black, spaceAfter=0))
    styles.add(ParagraphStyle(name='CellHead', parent=styles['Cell'], fontName='Helvetica-Bold'))
    styles.add(ParagraphStyle(name='CodeBlock', fontName='Courier', fontSize=8,
                              leading=11, spaceBefore=4, spaceAfter=11))
    styles.add(ParagraphStyle(name='BulletBody', parent=styles['ReportBody'], leftIndent=12,
                              firstLineIndent=-9, spaceAfter=5))
    lines = SOURCE.read_text().splitlines()
    story = []
    i = 0
    figure_number = 0
    while i < len(lines):
        ln = lines[i].strip()
        if not ln:
            i += 1
            continue
        if ln == '[TOC]':
            story.extend([PageBreak(), Paragraph('Contents', styles['Chapter'])])
            toc = TableOfContents()
            toc.levelStyles = [ParagraphStyle(name='TOCItem', fontName='Helvetica', fontSize=10,
                                              leading=18, leftIndent=0, firstLineIndent=0)]
            story.extend([toc, PageBreak()])
            i += 1
            continue
        if ln.startswith('### '):
            story.append(Paragraph(inline(ln[4:]), styles['Subheading']))
            i += 1
            continue
        if ln.startswith('## '):
            if ln == '## 12 Conclusion and future work':
                story.append(PageBreak())
            story.append(Paragraph(inline(ln[3:]), styles['Chapter']))
            i += 1
            continue
        if ln.startswith('# '):
            story.append(Paragraph(inline(ln[2:]), styles['ReportTitle']))
            i += 1
            continue
        if ln.startswith('```'):
            code = []
            i += 1
            while i < len(lines) and not lines[i].startswith('```'):
                code.append(lines[i])
                i += 1
            story.append(Preformatted('\n'.join(code), styles['CodeBlock'], maxLineLength=90))
            i += 1
            continue
        image = re.match(r'!\[([^]]+)\]\([^)]*/([^/]+)\.svg\)', ln)
        if image:
            figure_number += 1
            drawing = figs[image[2]]
            cap = Paragraph(f'Figure {figure_number}. {inline(image[1])}.', styles['Caption'])
            story.append(KeepTogether([Spacer(1, 5), drawing, cap]))
            i += 1
            continue
        if ln.startswith('|'):
            rows = []
            while i < len(lines) and lines[i].strip().startswith('|'):
                fields = [v.strip() for v in lines[i].strip().strip('|').split('|')]
                if not all(re.fullmatch(r'[:\- ]+', f) for f in fields):
                    rows.append(fields)
                i += 1
            n = len(rows[0])
            if n == 2:
                widths = [WIDTH * .27, WIDTH * .73]
            elif n == 3:
                widths = [WIDTH * .22, WIDTH * .34, WIDTH * .44]
            elif n == 4:
                widths = [WIDTH * .40] + [WIDTH * .20] * 3
            elif n == 5:
                widths = [WIDTH * .37] + [WIDTH * .1575] * 4
            elif n == 6:
                widths = [WIDTH * .28, WIDTH * .12, WIDTH * .13,
                          WIDTH * .12, WIDTH * .16, WIDTH * .19]
            else:
                widths = [WIDTH / n] * n
            content = [[Paragraph(inline(v), styles['CellHead' if ri == 0 else 'Cell'])
                        for v in row] for ri, row in enumerate(rows)]
            table = LongTable(content, colWidths=widths, repeatRows=1, hAlign='LEFT')
            table.setStyle(TableStyle([
                ('BACKGROUND', (0, 0), (-1, 0), PALE),
                ('GRID', (0, 0), (-1, -1), .5, GREY),
                ('VALIGN', (0, 0), (-1, -1), 'MIDDLE'),
                ('LEFTPADDING', (0, 0), (-1, -1), 7),
                ('RIGHTPADDING', (0, 0), (-1, -1), 7),
                ('TOPPADDING', (0, 0), (-1, -1), 7),
                ('BOTTOMPADDING', (0, 0), (-1, -1), 7),
            ]))
            story.extend([Spacer(1, 3), table, Spacer(1, 10)])
            continue
        chunk = [ln]
        i += 1
        while i < len(lines) and lines[i].strip() and not lines[i].startswith(('#', '|', '```', '![')):
            chunk.append(lines[i].strip())
            i += 1
        story.append(Paragraph(inline(' '.join(chunk)), styles['ReportBody']))
    target = OUT / 'Watchkeeper_Final_Project_Report.pdf'
    doc = Report(str(target), pagesize=A4, leftMargin=52, rightMargin=52,
                 topMargin=48, bottomMargin=57,
                 title='Watchkeeper Final Project Report',
                 author='Aditya Srivastava, Anshith Bhandary, Ananta Bose',
                 subject='Implementation architecture and evaluation of evidence driven recovery')
    frame = Frame(52, 57, WIDTH, A4[1] - 105, leftPadding=0, rightPadding=0,
                  topPadding=0, bottomPadding=0)
    doc.addPageTemplates(PageTemplate(id='Report', frames=[frame], onPage=footer))
    doc.multiBuild(story)
    print(target)


if __name__ == '__main__':
    build()
