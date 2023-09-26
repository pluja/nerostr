/** @type {import('tailwindcss').Config} */
module.exports = {
  content: [
    "./**/*.html",
  ],
  theme: {
    extend: {
      fontFamily: {
        'monocraft': ['monocraft', 'monospace'],
      },
    },
  },
  plugins: [
    require("@tailwindcss/typography")
  ],
}

