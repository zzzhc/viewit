// PostScript (DSC + language) grammar. highlight.js ships no PostScript
// grammar, so this covers the language's distinctive tokens — %-comments,
// /literal names, (string) literals, numbers, and the operator set.
// <<dict>> and <hex> forms are deliberately left unstyled: distinguishing
// them from hex strings needs full parsing, and hex blobs are font bitmap
// data with nothing to highlight inside.
export default function postscript(hljs) {
  const NAME = {
    className: 'symbol',
    begin: /\//,
    end: /(?=[\s(){}\[\]<>])/,
    relevance: 0
  }

  const STRING = {
    className: 'string',
    begin: /\(/,
    end: /\)/,
    contains: [hljs.BACKSLASH_ESCAPE]
  }

  const NUMBER = {
    className: 'number',
    variants: [
      { begin: /[+-]?(\d+\.\d*|\.\d+)([eE][+-]?\d+)?/ },
      { begin: /[+-]?\d+([eE][+-]?\d+)?/ }
    ],
    relevance: 0
  }

  return {
    name: 'PostScript',
    aliases: ['postscript', 'ps'],
    keywords: {
      literal: 'true false null',
      keyword: [
        'def bind load store if ifelse for forall repeat loop exit stop stopped quit run',
        'save restore gsave grestore grestoreall'
      ].join(' '),
      built_in: [
        // stack / arithmetic / comparison
        'dup exch pop copy index roll clear count mark cleartomark counttomark',
        'add sub mul div idiv mod abs neg ceiling floor round truncate sqrt exp ln log sin cos tan atan',
        'and or xor not bitshift eq ne ge gt le lt',
        // graphics state & paths
        'moveto lineto rmoveto rlineto curveto rcurveto arc arcn arct arcto newpath closepath stroke fill eofill',
        'setlinewidth setlinecap setlinejoin setdash setmiterlimit setgray setrgbcolor sethsbcolor setcmykcolor',
        'translate rotate scale concat setmatrix currentmatrix initmatrix transform dtransform itransform',
        'currentpoint currentlinewidth currentgray currentrgbcolor clip eoclip initclip',
        // fonts & text
        'findfont scalefont makefont setfont selectfont currentfont show ashow widthshow awidthshow kshow glyphshow stringwidth charpath',
        // arrays, dicts, strings
        'array string packedarray get put getinterval putinterval length aload astore',
        'dict begin end currentdict where systemdict userdict errordict',
        'type cvi cvr cvn cvx cvrs cvs execute exec anchorsearch search token',
        // files & i/o
        'file filter closefile read readhexstring readstring readline write writehexstring writestring bytesavailable flush status setfileposition fileposition',
        'image imagemask colorimage showpage copypage erasepage',
        'print pstack prompt echo version product realtime usertime vmreclaim vmstatus rand srand rrand'
      ].join(' ')
    },
    contains: [
      hljs.COMMENT('%', '$', { relevance: 0 }),
      NAME,
      STRING,
      NUMBER
    ]
  }
}
