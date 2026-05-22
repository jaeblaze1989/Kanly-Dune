module.exports = {
  content: ['./static/**/*.html'],
  theme: {
    extend: {
      colors: {
        kanly: {
          gold: '#dfb743',
          spice: '#ff6b00',
          void: '#030508'
        }
      },
      boxShadow: {
        spice: '0 40px 120px rgba(223, 183, 67, 0.18)',
        terminal: 'inset 0 0 20px rgba(0, 0, 0, 0.6)'
      }
    }
  },
  plugins: []
}
